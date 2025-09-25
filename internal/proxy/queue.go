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

type QueueConfig struct {
	MaxQueue        int
	MaxConcurrent   int
	EnqueueTimeout  time.Duration
	QueueWaitHeader bool
}

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

	waiters := make(chan struct{}, cfg.MaxQueue)      // queued-only
	active := make(chan struct{}, cfg.MaxConcurrent)  // running

	var depth int64 // queued depth only

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Try to enter the queue (queued-only). If full -> 429.
		select {
		case waiters <- struct{}{}:
			// ok
		default:
			imetrics.QueueRejectedInc()
			http.Error(w, "queue full, try again later", http.StatusTooManyRequests)
			return
		}

		queued := true
		newDepth := atomic.AddInt64(&depth, 1)
		imetrics.QueueDepthSet(newDepth)
		defer func() {
			if queued {
				<-waiters
				atomic.AddInt64(&depth, -1)
				imetrics.QueueDepthSet(atomic.LoadInt64(&depth))
			}
		}()

		// Race slot acquisition against timeout/cancel with deterministic priority.
		// We use a goroutine that *only* tries to acquire the active slot and is cancelable.
		ctx := r.Context()
		queueCtx, cancelAcquire := context.WithCancel(ctx)
		defer cancelAcquire()

		slotCh := make(chan struct{}, 1)
		go func() {
			// Try to acquire active unless canceled.
			select {
			case active <- struct{}{}:
				slotCh <- struct{}{}
			case <-queueCtx.Done():
				// canceled (timeout or client cancel) — do not acquire
			}
		}()

		timer := time.NewTimer(cfg.EnqueueTimeout)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			// Client canceled while queued
			cancelAcquire() // ensure acquire goroutine stops
			imetrics.QueueWaitObserve(time.Since(start))
			failQueue(w, ctx.Err())
			return

		case <-timer.C:
			// Timed out while queued
			cancelAcquire() // ensure acquire goroutine stops
			imetrics.QueueTimeoutsInc()
			imetrics.QueueWaitObserve(time.Since(start))
			failQueue(w, context.DeadlineExceeded)
			return

		case <-slotCh:
			// We got an active slot *before* timeout/cancel.
		}

		// Leave the queue now that we're active.
		<-waiters
		atomic.AddInt64(&depth, -1)
		imetrics.QueueDepthSet(atomic.LoadInt64(&depth))
		queued = false

		// Release active when done serving.
		defer func() { <-active }()

		if cfg.QueueWaitHeader {
			w.Header().Set("X-Concurrency-Limit", strconv.Itoa(cfg.MaxConcurrent))
			w.Header().Set("X-Queue-Limit", strconv.Itoa(cfg.MaxQueue))
			w.Header().Set("X-Queue-Depth", strconv.FormatInt(newDepth, 10))
			w.Header().Set("X-Queue-Wait", time.Since(start).String())
		}

		// Record queue wait for successful admission
		imetrics.QueueWaitObserve(time.Since(start))

		next.ServeHTTP(w, r)
	})
}

func failQueue(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	msg := "request cancelled while waiting in queue"
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "timed out while waiting in queue"
	}
	http.Error(w, msg, status)
}
