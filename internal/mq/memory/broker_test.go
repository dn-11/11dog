package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/11dog/11dog/internal/model"
	"github.com/11dog/11dog/internal/mq"
)

func TestMemoryBrokerBoundedQueueAndOverload(t *testing.T) {
	b := NewBroker(2)
	defer b.Close()

	ctx := context.Background()
	t1 := &mq.QueuedSubtask{TaskID: "t1", SubtaskID: "s1", NodeID: "n1", Deadline: time.Now().Add(time.Minute)}
	t2 := &mq.QueuedSubtask{TaskID: "t1", SubtaskID: "s2", NodeID: "n1", Deadline: time.Now().Add(time.Minute)}
	t3 := &mq.QueuedSubtask{TaskID: "t1", SubtaskID: "s3", NodeID: "n1", Deadline: time.Now().Add(time.Minute)}

	if err := b.EnqueueTask(ctx, "n1", t1); err != nil {
		t.Fatalf("enqueue 1 failed: %v", err)
	}
	if err := b.EnqueueTask(ctx, "n1", t2); err != nil {
		t.Fatalf("enqueue 2 failed: %v", err)
	}
	if err := b.EnqueueTask(ctx, "n1", t3); !errors.Is(err, mq.ErrQueueFull) {
		t.Fatalf("enqueue 3 = %v, want ErrQueueFull", err)
	}
	if depth := b.QueueDepth("n1"); depth != 2 {
		t.Fatalf("QueueDepth = %d, want 2", depth)
	}

	// Consume one and verify space is freed.
	consumeCtx, cancel := context.WithCancel(ctx)
	var consumed []string
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = b.ConsumeNodeQueue(consumeCtx, "n1", 1, func(_ context.Context, st *mq.QueuedSubtask) error {
			mu.Lock()
			consumed = append(consumed, st.SubtaskID)
			mu.Unlock()
			if len(consumed) >= 2 {
				cancel()
			}
			return nil
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for consumer")
	}

	if len(consumed) != 2 || consumed[0] != "s1" || consumed[1] != "s2" {
		t.Fatalf("consumed = %v, want [s1 s2]", consumed)
	}
}

func TestMemoryBrokerRequeueOnError(t *testing.T) {
	b := NewBroker(4)
	defer b.Close()

	ctx := context.Background()
	_ = b.EnqueueTask(ctx, "n1", &mq.QueuedSubtask{
		TaskID:    "t1",
		SubtaskID: "s1",
		NodeID:    "n1",
		Deadline:  time.Now().Add(time.Minute),
	})

	consumeCtx, cancel := context.WithCancel(ctx)
	_ = b.ConsumeNodeQueue(consumeCtx, "n1", 1, func(_ context.Context, _ *mq.QueuedSubtask) error {
		cancel()
		return errors.New("stream disconnected")
	})

	if depth := b.QueueDepth("n1"); depth != 1 {
		t.Fatalf("expected failed task to be requeued, depth=%d", depth)
	}
}

func TestMemoryBrokerPubSub(t *testing.T) {
	b := NewBroker(10)
	defer b.Close()

	ctx := context.Background()
	ch1, unsub1, err := b.SubscribeResult(ctx, "task-x")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub1()

	ch2, unsub2, err := b.SubscribeResult(ctx, "task-x")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub2()

	ev := &model.TaskEvent{EventType: "result", TaskID: "task-x", NodeID: "n1", Status: model.StatusCompleted}
	if err := b.PublishResult(ctx, ev); err != nil {
		t.Fatal(err)
	}

	for i, ch := range []<-chan *model.TaskEvent{ch1, ch2} {
		select {
		case got := <-ch:
			if got.NodeID != "n1" {
				t.Fatalf("sub %d got NodeID=%q, want n1", i, got.NodeID)
			}
		case <-time.After(time.Second):
			t.Fatalf("sub %d timed out", i)
		}
	}
}
