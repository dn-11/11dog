package nats

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dn-11/11dog/internal/model"
	"github.com/dn-11/11dog/internal/mq"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Options configures the NATS JetStream broker connection and stream parameters.
type Options struct {
	URL             string
	UserJWT         string
	UserSeed        string
	TLSConfig       *tls.Config
	StreamName      string
	MaxQueuePerNode int
	TaskTTL         time.Duration
}

// Broker implements mq.Broker backed by NATS JetStream (bounded per-node task streams)
// and Core NATS Pub/Sub (cross-replica real-time task event fan-out).
type Broker struct {
	nc              *natsgo.Conn
	js              jetstream.JetStream
	streamName      string
	maxQueuePerNode int
	taskTTL         time.Duration

	mu     sync.RWMutex
	closed bool
}

// NewBroker connects to NATS using decentralized JWT/NKey credentials and provisions the bounded JetStream task stream.
func NewBroker(ctx context.Context, opts Options) (*Broker, error) {
	if opts.StreamName == "" {
		opts.StreamName = "DOG_TASKS"
	}
	if opts.MaxQueuePerNode <= 0 {
		opts.MaxQueuePerNode = 20
	}
	if opts.TaskTTL <= 0 {
		opts.TaskTTL = 60 * time.Second
	}

	dialOpts := []natsgo.Option{
		natsgo.Name("11dog-server"),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(2 * time.Second),
	}
	if opts.UserJWT != "" && opts.UserSeed != "" {
		dialOpts = append(dialOpts, natsgo.UserJWTAndSeed(opts.UserJWT, opts.UserSeed))
	}
	if opts.TLSConfig != nil {
		dialOpts = append(dialOpts, natsgo.Secure(opts.TLSConfig))
	}

	nc, err := natsgo.Connect(opts.URL, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("connect nats %q: %w", opts.URL, err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("init jetstream: %w", err)
	}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:                 opts.StreamName,
		Subjects:             []string{"tasks.node.*"},
		Retention:            jetstream.LimitsPolicy,
		MaxAge:               opts.TaskTTL,
		MaxMsgsPerSubject:    int64(opts.MaxQueuePerNode),
		Discard:              jetstream.DiscardNew,
		DiscardNewPerSubject: true,
		Storage:              jetstream.MemoryStorage,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("provision jetstream stream %q: %w", opts.StreamName, err)
	}

	return &Broker{
		nc:              nc,
		js:              js,
		streamName:      opts.StreamName,
		maxQueuePerNode: opts.MaxQueuePerNode,
		taskTTL:         opts.TaskTTL,
	}, nil
}

// Conn returns the underlying NATS connection for KV, heartbeats, and control subjects.
func (b *Broker) Conn() *natsgo.Conn { return b.nc }

// JetStream returns the JetStream context.
func (b *Broker) JetStream() jetstream.JetStream { return b.js }

// StreamName returns the configured task stream name.
func (b *Broker) StreamName() string { return b.streamName }

func nodeSubject(nodeID uuid.UUID) string {
	return "tasks.node." + nodeID.String()
}

func resultSubject(taskID string) string {
	return "results.task." + taskID
}

func isQueueFullError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) {
		// 10077: maximum messages per subject exceeded; 10047: stream max msgs exceeded
		if apiErr.ErrorCode == 10077 || apiErr.ErrorCode == 10047 {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "maximum messages") || strings.Contains(msg, "max_msgs")
}

// EnqueueTask publishes a subtask to JetStream subject tasks.node.<nodeID>.
func (b *Broker) EnqueueTask(ctx context.Context, nodeID uuid.UUID, task *mq.QueuedSubtask) error {
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return mq.ErrClosed
	}

	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal subtask: %w", err)
	}

	_, err = b.js.Publish(ctx, nodeSubject(nodeID), payload, jetstream.WithMsgID(task.SubtaskID))
	if err != nil {
		if isQueueFullError(err) {
			return mq.ErrQueueFull
		}
		return fmt.Errorf("jetstream publish: %w", err)
	}
	return nil
}

// ConsumeNodeQueue pulls tasks from nodeID's durable JetStream consumer.
func (b *Broker) ConsumeNodeQueue(ctx context.Context, nodeID uuid.UUID, maxInFlight int, handler func(context.Context, *mq.QueuedSubtask) error) error {
	if maxInFlight <= 0 {
		maxInFlight = 8
	}
	consumer, err := b.js.CreateOrUpdateConsumer(ctx, b.streamName, jetstream.ConsumerConfig{
		Durable:       "node_" + nodeID.String(),
		FilterSubject: nodeSubject(nodeID),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       b.taskTTL + 15*time.Second,
		MaxAckPending: maxInFlight,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("create consumer for node %q: %w", nodeID, err)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b.mu.RLock()
		closed := b.closed
		b.mu.RUnlock()
		if closed {
			return mq.ErrClosed
		}

		msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		for msg := range msgs.Messages() {
			var subtask mq.QueuedSubtask
			if err := json.Unmarshal(msg.Data(), &subtask); err != nil {
				_ = msg.Term()
				continue
			}
			if err := handler(ctx, &subtask); err != nil {
				if subtask.Deadline.IsZero() || time.Now().Before(subtask.Deadline) {
					_ = msg.NakWithDelay(500 * time.Millisecond)
				} else {
					_ = msg.Ack()
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			_ = msg.Ack()
		}
	}
}

// PublishResult publishes task events over Core NATS so all server replicas receive them.
func (b *Broker) PublishResult(ctx context.Context, event *model.TaskEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return mq.ErrClosed
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal task event: %w", err)
	}
	return b.nc.Publish(resultSubject(event.TaskID), payload)
}

// SubscribeResult subscribes to Core NATS result events for taskID.
func (b *Broker) SubscribeResult(_ context.Context, taskID string) (<-chan *model.TaskEvent, func(), error) {
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return nil, nil, mq.ErrClosed
	}

	ch := make(chan *model.TaskEvent, 256)
	sub, err := b.nc.Subscribe(resultSubject(taskID), func(msg *natsgo.Msg) {
		var ev model.TaskEvent
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			return
		}
		select {
		case ch <- &ev:
		default:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("nats subscribe: %w", err)
	}

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			_ = sub.Unsubscribe()
		})
	}
	return ch, cancel, nil
}

// QueueDepth queries JetStream consumer pending messages for nodeID.
func (b *Broker) QueueDepth(nodeID uuid.UUID) int {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	consumer, err := b.js.Consumer(ctx, b.streamName, "node_"+nodeID.String())
	if err != nil {
		return 0
	}
	info, err := consumer.Info(ctx)
	if err != nil {
		return 0
	}
	return int(info.NumPending)
}

// Close drains and closes the underlying NATS connection.
func (b *Broker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	_ = b.nc.Drain()
	b.nc.Close()
	return nil
}
