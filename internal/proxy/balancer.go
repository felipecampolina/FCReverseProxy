package proxy

import (
	"net/url"
	"strings"
	"sync/atomic"
)

type Balancer interface {
	// Pick selects a target. If preview=true it MUST NOT alter active connection
	// counters (used for cache key pre-selection).
	Pick(preview bool) *url.URL
	// Acquire marks a real upstream request start for the chosen target and returns
	// a release func to call when done.
	Acquire(t *url.URL) func()
	Targets() []*url.URL
	Strategy() string
}

// ----- Round Robin -----

type roundRobinBalancer struct {
	targets []*url.URL
	idx     uint64
}

func NewRoundRobinBalancer(ts []*url.URL) Balancer {
	cp := append([]*url.URL{}, ts...)
	return &roundRobinBalancer{targets: cp}
}

func (b *roundRobinBalancer) Pick(preview bool) *url.URL {
	n := atomic.LoadUint64(&b.idx)
	if !preview {
		n = atomic.AddUint64(&b.idx, 1) - 1
	}
	return b.targets[n%uint64(len(b.targets))]
}

func (b *roundRobinBalancer) Acquire(_ *url.URL) func() { return func() {} }
func (b *roundRobinBalancer) Targets() []*url.URL       { return b.targets }
func (b *roundRobinBalancer) Strategy() string          { return "round_robin" }

// ----- Least Connections -----

type lcState struct {
	u      *url.URL
	active int64
}

type leastConnectionsBalancer struct {
	states []*lcState
}

func NewLeastConnectionsBalancer(ts []*url.URL) Balancer {
	states := make([]*lcState, 0, len(ts))
	for _, u := range ts {
		states = append(states, &lcState{u: u})
	}
	return &leastConnectionsBalancer{states: states}
}

func (b *leastConnectionsBalancer) Pick(_ bool) *url.URL {
	// Choose state with minimum active (stable order)
	var best *lcState
	for _, st := range b.states {
		if best == nil || atomic.LoadInt64(&st.active) < atomic.LoadInt64(&best.active) {
			best = st
		}
	}
	return best.u
}

func (b *leastConnectionsBalancer) Acquire(t *url.URL) func() {
	var target *lcState
	for _, st := range b.states {
		if st.u == t {
			target = st
			break
		}
	}
	if target == nil {
		return func() {}
	}
	atomic.AddInt64(&target.active, 1)
	return func() {
		atomic.AddInt64(&target.active, -1)
	}
}

func (b *leastConnectionsBalancer) Targets() []*url.URL {
	out := make([]*url.URL, 0, len(b.states))
	for _, st := range b.states {
		out = append(out, st.u)
	}
	return out
}
func (b *leastConnectionsBalancer) Strategy() string { return "least_connections" }

// ----- Factory / Configuration -----

func newBalancer(strategy string, targets []*url.URL) Balancer {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "least_conn", "lc", "least-connections", "least_connections":
		return NewLeastConnectionsBalancer(targets)
	default:
		return NewRoundRobinBalancer(targets)
	}
}
