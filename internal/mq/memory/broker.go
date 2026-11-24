package memory

import (
	"context"
	"sync"
	"time"

	"github.com/11dog/11dog/internal/model"
	"github.com/11dog/11dog/internal/mq"
)

type nodeQueue struct {
	mu      sync.Mutex
	items   []*mq.QueuedSubtask
	notify  chan struct{}
	maxSize int
}

func newNodeQueue(maxSize int) *nodeQueue {
	return &nodeQueue{
		items:   make([]*mq.QueuedSubtask, 0, maxSize),
		notify:  make(chan struct{}, 1),
		maxSize: maxSize,
	}
}

func (q *nodeQueue) pushBack(task *mq.QueuedSubtask) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Prune already-expired head items opportunistically to avoid false overload rejections.
	now := time.Now()
	for len(q.items) > 0 && !q.items[0].Deadline.IsZero() && now.After(q.items[0].Deadline) {
		q.items = q.items[1:]
	}
	if len(q.items) >= q.maxSize {
		return mq.ErrQueueFull
	}
	q.items = append(q.items, task)
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return nil
}

func (q *nodeQueue) pushFront(task *mq.QueuedSubtask) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.maxSize {
		return
	}
	q.items = append([]*mq.QueuedSubtask{task}, q.items...)
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *nodeQueue) pop() (*mq.QueuedSubtask, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil, false
	}
	item := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	if len(q.items) > 0 {
		select {
		case q.notify <- struct{}{}:
		default:
		}
	}
	return item, true
}

func (q *nodeQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Broker implements mq.Broker in memory with per-node bounded queues and task-ID pub/sub.
type Broker struct {
	maxQueuePerNode int

	mu     sync.RWMutex
	closed bool
	closeCh chan struct{}
	queues map[string]*nodeQueue

	subMu sync.RWMutex
	subs  map[string]map[uint64]chan *model.TaskEvent
	nextSubID uint64
}

// NewBroker constructs an in-memory bounded MQ broker.
func NewBroker(maxQueuePerNode int) *Broker {
	if maxQueuePerNode <= 0 {
		maxQueuePerNode = 20
	}
	return &Broker{
		maxQueuePerNode: maxQueuePerNode,
		closeCh:         make(chan struct{}),
		queues:          make(map[string]*nodeQueue),
		subs:            make(map[string]map[uint64]chan *model.TaskEvent),
	}
}

func (b *Broker) getOrCreateQueue(nodeID string) (*nodeQueue, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, mq.ErrClosed
	}
	q, ok := b.queues[nodeID]
	if !ok {
		q = newNodeQueue(b.maxQueuePerNode)
		b.queues[nodeID] = q
	}
	return q, nil
}

// EnqueueTask pushes task into nodeID's bounded FIFO queue.
func (b *Broker) EnqueueTask(ctx context.Context, nodeID string, task *mq.QueuedSubtask) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q, err := b.getOrCreateQueue(nodeID)
	if err != nil {
		return err
	}
	return q.pushBack(task)
}

// ConsumeNodeQueue pulls tasks from nodeID's queue and invokes handler sequentially.
func (b *Broker) ConsumeNodeQueue(ctx context.Context, nodeID string, _ int, handler func(context.Context, *mq.QueuedSubtask) error) error {
	q, err := b.getOrCreateQueue(nodeID)
	if err != nil {
		return err
	}
	for {
		if task, ok := q.pop(); ok {
			if err := handler(ctx, task); err != nil {
				// Requeue at the front unless already expired so reconnects don't lose buffered tasks.
				if task.Deadline.IsZero() || time.Now().Before(task.Deadline) {
					q.pushFront(task)
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.closeCh:
			return mq.ErrClosed
		case <-q.notify:
		}
	}
}

// PublishResult fans out an event to all active subscribers of event.TaskID.
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

	b.subMu.RLock()
	taskSubs := b.subs[event.TaskID]
	targets := make([]chan *model.TaskEvent, 0, len(taskSubs))
	for _, ch := range taskSubs {
		targets = append(targets, ch)
	}
	b.subMu.RUnlock()

	for _, ch := range targets {
		select {
		case ch <- event:
		default:
			// Drop oldest or skip if subscriber is terribly slow to avoid blocking producers.
		}
	}
	return nil
}

// SubscribeResult registers a buffered channel to receive TaskEvents for taskID.
func (b *Broker) SubscribeResult(_ context.Context, taskID string) (<-chan *model.TaskEvent, func(), error) {
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return nil, nil, mq.ErrClosed
	}

	ch := make(chan *model.TaskEvent, 256)
	b.subMu.Lock()
	b.nextSubID++
	subID := b.nextSubID
	if b.subs[taskID] == nil {
		b.subs[taskID] = make(map[uint64]chan *model.TaskEvent)
	}
	b.subs[taskID][subID] = ch
	b.subMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.subMu.Lock()
			if m := b.subs[taskID]; m != nil {
				delete(m, subID)
				if len(m) == 0 {
					delete(b.subs, taskID)
				}
			}
			b.subMu.Unlock()
		})
	}
	return ch, cancel, nil
}

// QueueDepth returns the current number of buffered subtasks for nodeID.
func (b *Broker) QueueDepth(nodeID string) int {
	b.mu.RLock()
	q := b.queues[nodeID]
	b.mu.RUnlock()
	if q == nil {
		return 0
	}
	return q.len()
}

// Close closes the broker and terminates active consumers.
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	close(b.closeCh)
	return nil
}
