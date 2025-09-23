package proxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

type QueueConfig struct {
	MaxQueue        int           // max number of requests allowed to wait (FIFO)
	MaxConcurrent   int           // max number of concurrent requests hitting upstream
	EnqueueTimeout  time.Duration // how long a request is willing to wait to ENTER the queue
	QueueWaitHeader bool          // emit X-Queue-* headers
}

// WithQueue wraps an http.Handler with a bounded FIFO waiting queue + concurrency cap.
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

	queueSlots := make(chan struct{}, cfg.MaxQueue)  // who is allowed to wait
	active := make(chan struct{}, cfg.MaxConcurrent) // who is allowed to run
	var depth int64                                  // fast path for depth metric/header

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Try to ENTER the queue within EnqueueTimeout.
		ctx, cancel := context.WithTimeout(r.Context(), cfg.EnqueueTimeout)
		defer cancel()

		select {
		case queueSlots <- struct{}{}:
			// we are allowed to wait; track depth
			newDepth := atomic.AddInt64(&depth, 1)
			defer func() {
				<-queueSlots
				atomic.AddInt64(&depth, -1)
			}()
			// While queued, continue observing caller cancellation
			select {
			case active <- struct{}{}: // reached front of queue and got an active slot
				defer func() { <-active }()
				if cfg.QueueWaitHeader {
					w.Header().Set("X-Concurrency-Limit", strconv.Itoa(cfg.MaxConcurrent))
					w.Header().Set("X-Queue-Limit", strconv.Itoa(cfg.MaxQueue))
					w.Header().Set("X-Queue-Depth", strconv.FormatInt(newDepth, 10))
					w.Header().Set("X-Queue-Wait", time.Since(start).String())
				}
				// hand off to the actual handler (proxy)
				next.ServeHTTP(w, r)
			case <-r.Context().Done():
				failQueue(w, r.Context().Err())
				return
			}
		case <-ctx.Done():
			// Could not even ENTER the queue in time (full/slow). Return 429 to signal backoff.
			http.Error(w, "queue full, try again later", http.StatusTooManyRequests)
			return
		}
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
