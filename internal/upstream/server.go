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
)

// Item represents a simple record stored in memory.
type Item struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Value     int       `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// store is an in-memory data store with basic CRUD.
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

func (s *store) list() []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Item, 0, len(s.data))
	for _, v := range s.data {
		out = append(out, v)
	}
	return out
}

func (s *store) get(id int) (Item, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, ok := s.data[id]
	return it, ok
}

func (s *store) create(name string, value int) Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	it := Item{ID: id, Name: name, Value: value, UpdatedAt: time.Now()}
	s.data[id] = it
	return it
}

func (s *store) update(id int, name string, value int) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.data[id]
	if !ok {
		return Item{}, false
	}
	if strings.TrimSpace(name) != "" {
		it.Name = name
	}
	it.Value = value
	it.UpdatedAt = time.Now()
	s.data[id] = it
	return it, true
}

func (s *store) delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[id]; !ok {
		return false
	}
	delete(s.data, id)
	return true
}

var requestCounter int64

// Start boots the upstream example server on the provided address.
func Start(addr string) error {
	mem := newStore()
	// Seed with a couple of items
	mem.create("alpha", 10)
	mem.create("beta", 20)

	mux := http.NewServeMux()

	// Health endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Cacheable endpoint to test proxy caching
	mux.HandleFunc("/cache", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		w.Header().Set("Cache-Control", "public, max-age=10, s-maxage=10")
		writeJSON(w, http.StatusOK, map[string]any{
			"endpoint": "cache",
			"now":      time.Now().Format(time.RFC3339Nano),
		})
	})

	// Slow endpoint to observe cache impact
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		time.Sleep(1200 * time.Millisecond)
		w.Header().Set("Cache-Control", "public, max-age=10, s-maxage=10")
		writeJSON(w, http.StatusOK, map[string]any{
			"endpoint": "slow",
			"now":      time.Now().Format(time.RFC3339Nano),
		})
	})

	// Landing route (now cacheable for shared caches)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		w.Header().Set("Cache-Control", "public, max-age=10, s-maxage=10")
		_, _ = w.Write([]byte("Upstream server is running.\n"))
	})

	// Items API list/create
	mux.HandleFunc("/api/items", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, mem.list())
		case http.MethodPost:
			var in struct {
				Name  string `json:"name"`
				Value int    `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(in.Name) == "" {
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			it := mem.create(in.Name, in.Value)
			w.Header().Set("Location", fmt.Sprintf("/api/items/%d", it.ID))
			writeJSON(w, http.StatusCreated, it)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Items API get/update/delete
	mux.HandleFunc("/api/items/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ method=%s url=%s", r.Method, r.URL.Path)
		// path: /api/items/{id}
		id, ok := parseID(strings.TrimPrefix(r.URL.Path, "/api/items/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if it, ok := mem.get(id); ok {
				writeJSON(w, http.StatusOK, it)
				return
			}
			http.NotFound(w, r)
		case http.MethodPut:
			var in struct {
				Name  string `json:"name"`
				Value int    `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			if it, ok := mem.update(id, in.Name, in.Value); ok {
				writeJSON(w, http.StatusOK, it)
				return
			}
			http.NotFound(w, r)
		case http.MethodDelete:
			if mem.delete(id) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.NotFound(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Acquire listener first so we can handle "address in use" gracefully.
	l, err := net.Listen("tcp", addr)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		fallback := addrWithPortZero(addr)
		log.Printf("Address %q in use, retrying on %q", addr, fallback)
		l, err = net.Listen("tcp", fallback)
	}
	if err != nil {
		return err
	}
	log.Printf("Upstream example server listening on %s", l.Addr().String())
	// Wrap with logging and request ID middleware.
	return http.Serve(l, withRequestID(withRequestLogging(withServerHeaders(mux))))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func parseID(s string) (int, bool) {
	id, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func withServerHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "upstream/0.1")
		next.ServeHTTP(w, r)
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
