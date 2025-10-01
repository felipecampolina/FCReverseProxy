package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	applog "traefik-challenge-2/internal/log"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Item represents a simple record stored in memory.
type Item struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Value     int       `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// store is an in-memory data store with basic CRUD operations.
type store struct {
	mu     sync.RWMutex
	nextID int
	data   map[int]Item
}

func newStore() *store {
	return &store{
		nextID: 1,
		data:   make(map[int]Item),
	}
}

// list returns all items currently stored.
func (dataStore *store) list() []Item {
	dataStore.mu.RLock()
	defer dataStore.mu.RUnlock()
	out := make([]Item, 0, len(dataStore.data))
	for _, v := range dataStore.data {
		out = append(out, v)
	}
	return out
}

// get retrieves an item by ID.
func (dataStore *store) get(id int) (Item, bool) {
	dataStore.mu.RLock()
	defer dataStore.mu.RUnlock()
	item, ok := dataStore.data[id]
	return item, ok
}

// create inserts a new item with the provided name and value.
func (dataStore *store) create(name string, value int) Item {
	dataStore.mu.Lock()
	defer dataStore.mu.Unlock()
	id := dataStore.nextID
	dataStore.nextID++
	item := Item{ID: id, Name: name, Value: value, UpdatedAt: time.Now()}
	dataStore.data[id] = item
	return item
}

// update modifies an existing item with the provided name and value.
func (dataStore *store) update(id int, name string, value int) (Item, bool) {
	dataStore.mu.Lock()
	defer dataStore.mu.Unlock()
	item, ok := dataStore.data[id]
	if !ok {
		return Item{}, false
	}
	if strings.TrimSpace(name) != "" {
		item.Name = name
	}
	item.Value = value
	item.UpdatedAt = time.Now()
	dataStore.data[id] = item
	return item, true
}

// delete removes an item by ID.
func (dataStore *store) delete(id int) bool {
	dataStore.mu.Lock()
	defer dataStore.mu.Unlock()
	if _, ok := dataStore.data[id]; !ok {
		return false
	}
	delete(dataStore.data, id)
	return true
}

// Start boots the upstream example server on the provided address.
// This server is for demonstration purposes only.
func Start(listenAddr string) error {
	dataStore := newStore()
	// Seed with a couple of items
	dataStore.create("alpha", 10)
	dataStore.create("beta", 20)

	mux := http.NewServeMux()

	// Metrics endpoint served on the same listener (no separate port/env needed).
	mux.Handle("/metrics", promhttp.Handler())

	// Health endpoint.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Cacheable endpoint to test proxy caching.
	mux.HandleFunc("/cache", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		w.Header().Set("Cache-Control", "public, max-age=10, s-maxage=10")
		writeJSON(w, http.StatusOK, map[string]any{
			"endpoint": "cache",
			"now":      time.Now().Format(time.RFC3339Nano),
		})
	})

	// Slow endpoint to observe cache impact.
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		time.Sleep(1200 * time.Millisecond)
		w.Header().Set("Cache-Control", "public, max-age=10, s-maxage=10")
		writeJSON(w, http.StatusOK, map[string]any{
			"endpoint": "slow",
			"now":      time.Now().Format(time.RFC3339Nano),
		})
	})

	// Landing route (cacheable for shared caches).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		w.Header().Set("Cache-Control", "public, max-age=10, s-maxage=10")
		_, _ = w.Write([]byte("Upstream server is running.\n"))
	})

	// Items API list/create.
	mux.HandleFunc("/api/items", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, dataStore.list())
		case http.MethodPost:
			var input struct {
				Name  string `json:"name"`
				Value int    `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(input.Name) == "" {
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			item := dataStore.create(input.Name, input.Value)
			w.Header().Set("Location", fmt.Sprintf("/api/items/%d", item.ID))
			writeJSON(w, http.StatusCreated, item)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Items API get/update/delete.
	mux.HandleFunc("/api/items/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		// path: /api/items/{id}
		itemID, ok := parseID(strings.TrimPrefix(r.URL.Path, "/api/items/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if item, found := dataStore.get(itemID); found {
				writeJSON(w, http.StatusOK, item)
				return
			}
			http.NotFound(w, r)
		case http.MethodPut:
			var input struct {
				Name  string `json:"name"`
				Value int    `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			if item, updated := dataStore.update(itemID, input.Name, input.Value); updated {
				writeJSON(w, http.StatusOK, item)
				return
			}
			http.NotFound(w, r)
		case http.MethodDelete:
			if dataStore.delete(itemID) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.NotFound(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Acquire listener first so we can handle "address in use" gracefully.
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		fallbackAddr := addrWithPortZero(listenAddr)
		log.Printf("Address %q in use, retrying on %q", listenAddr, fallbackAddr)
		listener, err = net.Listen("tcp", fallbackAddr)
	}
	if err != nil {
		return err
	}

	log.Printf("Upstream example server listening on %s", listener.Addr().String())

	// Build middleware chain and inject upstream ID header.
	upstreamID := listener.Addr().String()
	handlerChain := applog.WithRequestID(
		applog.WithRequestLogging(
			withServerHeaders(
				withUpstreamHeader(upstreamID, mux),
			),
		),
	)

	return http.Serve(listener, handlerChain)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// parseID parses a positive integer ID from a string.
func parseID(s string) (int, bool) {
	id, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// withServerHeaders adds a fixed Server header for all responses.
func withServerHeaders(nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "upstream/0.1")
		nextHandler.ServeHTTP(w, r)
	})
}

// addrWithPortZero returns the same host with port 0 (ephemeral). If parsing fails, returns ":0".
func addrWithPortZero(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ":0"
	}
	return net.JoinHostPort(host, "0")
}

// withUpstreamHeader injects the X-Upstream header for every response.
func withUpstreamHeader(upstreamID string, nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", upstreamID)
		nextHandler.ServeHTTP(w, r)
	})
}
