package proxy

import (
	"fmt"
	"net/url"
	"sync"
	"testing"
)

var (
	_testFileBannerMu     sync.Mutex
	_testFileBannerPrinted = map[string]struct{}{}
)
func init() {
	// Ensure test output is not interleaved
	banner("balancer_test.go")
}
func banner(file string) {
	_testFileBannerMu.Lock()
	if _, ok := _testFileBannerPrinted[file]; ok {
		_testFileBannerMu.Unlock()
		return
	}
	_testFileBannerPrinted[file] = struct{}{}
	_testFileBannerMu.Unlock()
	fmt.Printf("\n===== BEGIN TEST FILE: internal/proxy/%s =====\n", file)
}

func mustURL(t *testing.T, raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return u
}

func TestRoundRobinBalancer(t *testing.T) {
	targets := []*url.URL{
		mustURL(t, "http://one"),
		mustURL(t, "http://two"),
		mustURL(t, "http://three"),
	}
	// Disable health checks in tests
	b := NewRoundRobinBalancer(targets, false)

	seq := []string{}
	for i := 0; i < 6; i++ {
		u := b.Pick(false)
		b.Acquire(u)() // no-op
		seq = append(seq, u.Host)
	}
	want := []string{"one", "two", "three", "one", "two", "three"}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("rr order mismatch got=%v want=%v", seq, want)
		}
	}
}

func TestLeastConnectionsBalancerBasic(t *testing.T) {
	banner("balancer_test.go")
	targets := []*url.URL{
		mustURL(t, "http://a"),
		mustURL(t, "http://b"),
		mustURL(t, "http://c"),
	}
	// Disable health checks in tests 
	b := NewLeastConnectionsBalancer(targets, false)

	// First pick: all zero -> picks 'a'
	a := b.Pick(false)
	if a.Host != "a" {
		t.Fatalf("expected a first, got %s", a.Host)
	}
	releaseA1 := b.Acquire(a)

	// Second pick: a=1 b=0 c=0 -> should pick b
	bu := b.Pick(false)
	if bu.Host != "b" {
		t.Fatalf("expected b second, got %s", bu.Host)
	}
	releaseB1 := b.Acquire(bu)

	// Third pick: a=1 b=1 c=0 -> should pick c
	c := b.Pick(false)
	if c.Host != "c" {
		t.Fatalf("expected c third, got %s", c.Host)
	}
	releaseC1 := b.Acquire(c)

	// Release b so counts: a=1 b=0 c=1 -> next should pick b
	releaseB1()
	next := b.Pick(false)
	if next.Host != "b" {
		t.Fatalf("expected b after release, got %s", next.Host)
	}

	// Cleanup remaining
	releaseA1()
	releaseC1()
}
