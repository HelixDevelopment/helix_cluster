package messaging

import (
	"context"
	"fmt"
	"sync"
)

// Queue is an in-memory work queue with competing-consumer semantics: each
// enqueued Message is delivered to exactly one consumer, in FIFO order. This
// differs from Bus, which fans every message out to every subscriber.
//
// A Queue is bounded; Enqueue blocks when the buffer is full and unblocks once
// a consumer drains an item or the supplied context is cancelled. Closing the
// queue causes Dequeue to drain any remaining buffered items and then report
// closure, so no enqueued-and-accepted message is silently dropped on Close.
type Queue struct {
	ch     chan *Message
	mu     sync.Mutex
	closed bool
}

// NewQueue creates a Queue with the given buffer capacity. A capacity of zero
// yields an unbuffered queue where every Enqueue rendezvous-hands off directly
// to a waiting Dequeue.
func NewQueue(capacity int) *Queue {
	if capacity < 0 {
		capacity = 0
	}
	return &Queue{ch: make(chan *Message, capacity)}
}

// Enqueue appends a message to the queue. It blocks if the buffer is full and
// returns the context error if ctx is cancelled first, or an error if the
// queue has been closed. A successful (nil-error) return guarantees the message
// is buffered and will be delivered to exactly one Dequeue caller.
func (q *Queue) Enqueue(ctx context.Context, msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return fmt.Errorf("queue is closed")
	}
	q.mu.Unlock()

	select {
	case q.ch <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Dequeue removes and returns the next message in FIFO order. It blocks until a
// message is available, ctx is cancelled, or the queue is closed and drained.
// The boolean is false only when the queue is closed AND empty, signalling that
// no further messages will ever arrive.
func (q *Queue) Dequeue(ctx context.Context) (*Message, bool, error) {
	select {
	case msg, ok := <-q.ch:
		if !ok {
			return nil, false, nil
		}
		return msg, true, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// Len reports the number of messages currently buffered and not yet dequeued.
func (q *Queue) Len() int {
	return len(q.ch)
}

// Close marks the queue closed. Already-buffered messages remain dequeueable
// until drained; subsequent Enqueue calls fail. Close is idempotent.
func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	close(q.ch)
	return nil
}
