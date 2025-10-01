package proxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	imetrics "traefik-challenge-2/internal/metrics"
)

// QueueConfig controls the admission queue and concurrency limiter.
// - MaxQueue: maximum number of requests allowed to wait in the queue.
// - MaxConcurrent: maximum number of requests processed concurrently.
// - EnqueueTimeout: maximum time a request is allowed to wait before being rejected.
// - QueueWaitHeader: if true, emits headers with queue/concurrency metadata.
type QueueConfig struct {
	MaxQueue        int
	MaxConcurrent   int
	EnqueueTimeout  time.Duration
	QueueWaitHeader bool
}

// WithQueue wraps an http.Handler with a bounded waiting queue and a bounded
// concurrency limiter. Requests first try to enter the queue (bounded by MaxQueue).
// Once queued, they race to acquire an "active slot" (bounded by MaxConcurrent).
// While waiting, they can be canceled by the client or rejected after EnqueueTimeout.
// Metrics are emitted for queue depth, rejections, timeouts, and wait durations.
func WithQueue(next http.Handler, cfg QueueConfig) http.Handler {
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 1024
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 128
	}
	if cfg.EnqueueTimeout <= 0 {
		cfg.EnqueueTimeout = 2 * time.Second
	}

	// queueWaitCh tracks queued requests (waiting only).
	queueWaitCh := make(chan struct{}, cfg.MaxQueue)

	// activeSlotsCh tracks currently executing requests.
	activeSlotsCh := make(chan struct{}, cfg.MaxConcurrent)

	// queueDepth holds the current number of queued (not active) requests.
	var queueDepth int64

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enqueueStart := time.Now()

		// Try to enter the queue; if queue is full, reject immediately (429).
		select {
		case queueWaitCh <- struct{}{}:
			// Admitted into the queue.
		default:
			imetrics.QueueRejectedInc()
			http.Error(w, "queue full, try again later", http.StatusTooManyRequests)
			return
		}

		isStillQueued := true
		depthAfterEnqueue := atomic.AddInt64(&queueDepth, 1)
		imetrics.QueueDepthSet(depthAfterEnqueue)

		// Ensure queue bookkeeping is reverted if we exit before becoming active.
		defer func() {
			if isStillQueued {
				<-queueWaitCh
				atomic.AddInt64(&queueDepth, -1)
				imetrics.QueueDepthSet(atomic.LoadInt64(&queueDepth))
			}
		}()

		// We race "acquire active slot" against timeout/client-cancel.
		// Use a cancelable context dedicated to acquisition to avoid leaking the goroutine.
		reqCtx := r.Context()
		acquireCtx, cancelAcquire := context.WithCancel(reqCtx)
		defer cancelAcquire()

		activeGrantedCh := make(chan struct{}, 1)
		go func() {
			// Only acquire if not canceled by timeout or client.
			select {
			case activeSlotsCh <- struct{}{}:
				activeGrantedCh <- struct{}{}
			case <-acquireCtx.Done():
				// Canceled before acquiring an active slot.
			}
		}()

		enqueueTimer := time.NewTimer(cfg.EnqueueTimeout)
		defer enqueueTimer.Stop()

		// Deterministic selection: whichever happens first wins.
		select {
		case <-reqCtx.Done():
			// Client canceled while waiting in the queue.
			cancelAcquire()
			imetrics.QueueWaitObserve(time.Since(enqueueStart))
			failQueue(w, reqCtx.Err())
			return

		case <-enqueueTimer.C:
			// Timed out while waiting in the queue.
			cancelAcquire()
			imetrics.QueueTimeoutsInc()
			imetrics.QueueWaitObserve(time.Since(enqueueStart))
			failQueue(w, context.DeadlineExceeded)
			return

		case <-activeGrantedCh:
			// Successfully acquired an active (concurrency) slot.
		}

		// Transition from queued -> active.
		<-queueWaitCh
		atomic.AddInt64(&queueDepth, -1)
		imetrics.QueueDepthSet(atomic.LoadInt64(&queueDepth))
		isStillQueued = false

		// Release active slot once request is served.
		defer func() { <-activeSlotsCh }()

		// Optional observability headers.
		if cfg.QueueWaitHeader {
			w.Header().Set("X-Concurrency-Limit", strconv.Itoa(cfg.MaxConcurrent))
			w.Header().Set("X-Queue-Limit", strconv.Itoa(cfg.MaxQueue))
			w.Header().Set("X-Queue-Depth", strconv.FormatInt(depthAfterEnqueue, 10))
			w.Header().Set("X-Queue-Wait", time.Since(enqueueStart).String())
		}

		// Record queue wait for successfully admitted requests.
		imetrics.QueueWaitObserve(time.Since(enqueueStart))

		next.ServeHTTP(w, r)
	})
}

// failQueue maps queue wait errors to an HTTP response.
func failQueue(w http.ResponseWriter, err error) {
	httpStatus := http.StatusServiceUnavailable
	errorMsg := "request cancelled while waiting in queue"
	if errors.Is(err, context.DeadlineExceeded) {
		errorMsg = "timed out while waiting in queue"
	}
	http.Error(w, errorMsg, httpStatus)
}
