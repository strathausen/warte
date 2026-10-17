package backend

import (
	"sync"

	"warte/internal/core"
)

// eventQueue is an unbounded FIFO between producers (signalmeow's receive
// loop, background jobs, action methods) and the single consumer of
// Events(). push never blocks, so a slow frontend can never stall the
// Signal connection; a pump goroutine delivers events in order.
type eventQueue struct {
	out chan core.Event

	mu      sync.Mutex
	pending []core.Event
	wake    chan struct{}
	closed  bool
	done    chan struct{}
	stopped chan struct{}
}

func newEventQueue() *eventQueue {
	q := &eventQueue{
		out:     make(chan core.Event),
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go q.pump()
	return q
}

// push enqueues events. It is a no-op after close.
func (q *eventQueue) push(evs ...core.Event) {
	if len(evs) == 0 {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.pending = append(q.pending, evs...)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// len reports the number of undelivered events (for tests).
func (q *eventQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

func (q *eventQueue) pump() {
	defer close(q.stopped)
	defer close(q.out)
	for {
		q.mu.Lock()
		var ev core.Event
		if len(q.pending) > 0 {
			ev = q.pending[0]
			q.pending[0] = nil
			q.pending = q.pending[1:]
			if len(q.pending) == 0 {
				q.pending = nil // release the backing array
			}
		}
		q.mu.Unlock()
		if ev == nil {
			select {
			case <-q.wake:
				continue
			case <-q.done:
				return
			}
		}
		select {
		case q.out <- ev:
		case <-q.done:
			return
		}
	}
}

// close stops delivery and closes the output channel. Undelivered events
// are dropped.
func (q *eventQueue) close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.pending = nil
	q.mu.Unlock()
	close(q.done)
	<-q.stopped
}
