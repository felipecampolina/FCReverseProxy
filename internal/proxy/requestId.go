package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Add an atomic counter to help build unique request IDs.
var requestCounter int64

// ensureRequestID sets X-Request-ID on the request if missing and returns it.
func ensureRequestID(req *http.Request) string {
	requestID := strings.TrimSpace(req.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&requestCounter, 1))
		req.Header.Set("X-Request-ID", requestID)
	}
	return requestID
}

// getRequestID returns an existing X-Request-ID without generating a new one.
func getRequestID(req *http.Request) string {
	return strings.TrimSpace(req.Header.Get("X-Request-ID"))
}



