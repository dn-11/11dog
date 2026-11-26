package mq

import (
	"context"
	"errors"
	"time"

	"github.com/dn-11/11dog/internal/model"
	"github.com/google/uuid"
)

var (
	// ErrQueueFull is returned when a target edge node's bounded queue is at capacity.
	ErrQueueFull = errors.New("node task queue is full")
	// ErrClosed is returned when operating on a closed broker.
	ErrClosed = errors.New("mq broker is closed")
)

// QueuedSubtask represents a single-node probe task buffered in the MQ layer.
type QueuedSubtask struct {
	TaskID    string            `json:"task_id"`
	SubtaskID string            `json:"subtask_id"`
	NodeID    uuid.UUID         `json:"node_id"`
	Type      model.ProbeType   `json:"type"`
	Target    string            `json:"target"`
	TimeoutMs uint32            `json:"timeout_ms"`
	Params    model.ProbeParams `json:"params"`
	CreatedAt time.Time         `json:"created_at"`
	Deadline  time.Time         `json:"deadline"`
}

// Broker abstracts the per-node task buffering queue and task result pub/sub bus.
type Broker interface {
	// EnqueueTask pushes a subtask into nodeID's bounded queue. Returns ErrQueueFull on overflow.
	EnqueueTask(ctx context.Context, nodeID uuid.UUID, task *QueuedSubtask) error

	// PublishResult broadcasts a task event (progress/result/done) to all subscribers of taskID.
	PublishResult(ctx context.Context, event *model.TaskEvent) error

	// SubscribeResult returns a live channel of events for taskID and an unsubscribe cleanup callback.
	SubscribeResult(ctx context.Context, taskID string) (<-chan *model.TaskEvent, func(), error)

	// QueueDepth returns the approximate number of queued subtasks waiting for nodeID.
	QueueDepth(nodeID uuid.UUID) int

	// Close releases all broker resources.
	Close() error
}
