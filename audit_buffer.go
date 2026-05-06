package smplkit

import (
	"context"
	"log"
	"math/rand/v2"
	"sync"
	"time"

	genaudit "github.com/smplkit/go-sdk/v3/internal/generated/audit"
)

// auditEventBuffer is a bounded in-memory queue + worker goroutine for
// fire-and-forget audit emits. ADR-047 §2.6.
//
// enqueue returns immediately. The worker drains the queue on either a
// periodic tick or once depth crosses the high-water mark, retries
// transient failures with exponential backoff, drops permanent 4xx
// (other than 429) with a log line, and evicts the oldest item under
// overflow.
type auditEventBuffer struct {
	gen         *genaudit.ClientWithResponses
	maxSize     int
	watermark   int
	flushEvery  time.Duration
	maxAttempts int
	initialBack time.Duration
	maxBack     time.Duration

	mu      sync.Mutex
	queue   []*pendingAuditEvent
	dropped int
	closed  bool

	wake chan struct{}
	done chan struct{}
}

type pendingAuditEvent struct {
	body        genaudit.EventResponse
	idempKey    string
	attempts    int
	nextRetryAt time.Time
}

const (
	auditMaxBufferSize  = 1000
	auditWatermark      = 50
	auditFlushInterval  = 5 * time.Second
	auditMaxAttempts    = 5
	auditInitialBackoff = 250 * time.Millisecond
	auditMaxBackoff     = 8 * time.Second
)

func newAuditEventBuffer(gen *genaudit.ClientWithResponses) *auditEventBuffer {
	b := &auditEventBuffer{
		gen:         gen,
		maxSize:     auditMaxBufferSize,
		watermark:   auditWatermark,
		flushEvery:  auditFlushInterval,
		maxAttempts: auditMaxAttempts,
		initialBack: auditInitialBackoff,
		maxBack:     auditMaxBackoff,
		wake:        make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *auditEventBuffer) enqueue(body genaudit.EventResponse, idempKey string) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if len(b.queue) >= b.maxSize {
		b.queue = b.queue[1:]
		b.dropped++
		log.Printf("smplkit: audit buffer full (size=%d); dropped oldest event (total dropped=%d)", b.maxSize, b.dropped)
	}
	b.queue = append(b.queue, &pendingAuditEvent{body: body, idempKey: idempKey})
	depth := len(b.queue)
	b.mu.Unlock()
	if depth >= b.watermark {
		b.signalWake()
	}
}

func (b *auditEventBuffer) flush(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		b.mu.Lock()
		empty := len(b.queue) == 0
		b.mu.Unlock()
		if empty {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("smplkit: audit buffer flush timed out (timeout=%s)", timeout)
			return
		}
		b.signalWake()
		time.Sleep(50 * time.Millisecond)
	}
}

func (b *auditEventBuffer) close(timeout time.Duration) {
	b.flush(timeout)
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.signalWake()
	select {
	case <-b.done:
	case <-time.After(timeout):
	}
}

func (b *auditEventBuffer) signalWake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *auditEventBuffer) run() {
	defer close(b.done)
	for {
		b.drainOnce()
		b.mu.Lock()
		closed := b.closed && len(b.queue) == 0
		// Sleep until either the periodic tick OR the earliest pending
		// retry, whichever comes first. Without that, retries scheduled
		// inside the flush interval never fire until the next tick.
		sleep := b.flushEvery
		if len(b.queue) > 0 {
			head := b.queue[0]
			if !head.nextRetryAt.IsZero() {
				until := time.Until(head.nextRetryAt)
				if until > 0 && until < sleep {
					sleep = until
				}
			}
		}
		b.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-time.After(sleep):
		case <-b.wake:
		}
	}
}

func (b *auditEventBuffer) drainOnce() {
	now := time.Now()
	for {
		b.mu.Lock()
		if len(b.queue) == 0 {
			b.mu.Unlock()
			return
		}
		head := b.queue[0]
		if head.nextRetryAt.After(now) {
			b.mu.Unlock()
			return
		}
		b.queue = b.queue[1:]
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		params := &genaudit.CreateEventParams{}
		if head.idempKey != "" {
			ik := head.idempKey
			params.IdempotencyKey = &ik
		}
		resp, err := b.gen.CreateEventWithApplicationVndAPIPlusJSONBodyWithResponse(ctx, params, head.body)
		cancel()

		var status int
		if resp != nil {
			status = resp.StatusCode()
		}
		if requeue := b.handleOutcome(head, status, err); requeue != nil {
			b.mu.Lock()
			b.queue = append([]*pendingAuditEvent{requeue}, b.queue...)
			b.mu.Unlock()
			return
		}
	}
}

func (b *auditEventBuffer) handleOutcome(item *pendingAuditEvent, status int, err error) *pendingAuditEvent {
	if err == nil && status >= 200 && status < 300 {
		return nil
	}
	if err == nil && status >= 400 && status < 500 && status != 429 {
		log.Printf("smplkit: audit POST permanent failure status=%d; event dropped", status)
		return nil
	}
	item.attempts++
	if item.attempts >= b.maxAttempts {
		log.Printf("smplkit: audit POST gave up after %d attempts (last error=%v, status=%d)", item.attempts, err, status)
		return nil
	}
	backoff := b.initialBack * (1 << (item.attempts - 1))
	if backoff > b.maxBack {
		backoff = b.maxBack
	}
	jitter := time.Duration(rand.Int64N(int64(backoff / 4)))
	item.nextRetryAt = time.Now().Add(backoff + jitter)
	return item
}
