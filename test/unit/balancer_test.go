package proxy_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	proxy "traefik-challenge-2/internal/proxy"
)

var (
	// Mutex and map to ensure the banner for this test file is printed once and test logs aren't interleaved.
	bannerMu       sync.Mutex
	printedBanners = map[string]struct{}{}
)

func init() {
	// Ensure test output is not interleaved
	banner("balancer_test.go")
}

// banner prints a one-time banner per file to help visually separate test logs.
func banner(file string) {
	bannerMu.Lock()
	if _, ok := printedBanners[file]; ok {
		bannerMu.Unlock()
		return
	}
	printedBanners[file] = struct{}{}
	bannerMu.Unlock()
	fmt.Printf("\n===== BEGIN TEST FILE: internal/proxy/%s =====\n", file)
}

// mustURL parses a URL or fails the test immediately for brevity in test setup.
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
	rrBalancer := proxy.NewRoundRobinBalancer(targets, false)

	// Collect selected host order to validate round-robin behavior.
	selectedHosts := []string{}
	for i := 0; i < 6; i++ {
		pickedTarget := rrBalancer.Pick(false)
		rrBalancer.Acquire(pickedTarget)() // no-op
		selectedHosts = append(selectedHosts, pickedTarget.Host)
	}
	expectedHosts := []string{"one", "two", "three", "one", "two", "three"}
	for i := range expectedHosts {
		if selectedHosts[i] != expectedHosts[i] {
			t.Fatalf("rr order mismatch got=%v want=%v", selectedHosts, expectedHosts)
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
	lcBalancer := proxy.NewLeastConnectionsBalancer(targets, false)

	// First pick: all zero -> picks 'a'
	firstTarget := lcBalancer.Pick(false)
	if firstTarget.Host != "a" {
		t.Fatalf("expected a first, got %s", firstTarget.Host)
	}
	releaseFirst := lcBalancer.Acquire(firstTarget)

	// Second pick: a=1 b=0 c=0 -> should pick b
	secondTarget := lcBalancer.Pick(false)
	if secondTarget.Host != "b" {
		t.Fatalf("expected b second, got %s", secondTarget.Host)
	}
	releaseSecond := lcBalancer.Acquire(secondTarget)

	// Third pick: a=1 b=1 c=0 -> should pick c
	thirdTarget := lcBalancer.Pick(false)
	if thirdTarget.Host != "c" {
		t.Fatalf("expected c third, got %s", thirdTarget.Host)
	}
	releaseThird := lcBalancer.Acquire(thirdTarget)

	// Release b so counts: a=1 b=0 c=1 -> next should pick b
	releaseSecond()
	nextPick := lcBalancer.Pick(false)
	if nextPick.Host != "b" {
		t.Fatalf("expected b after release, got %s", nextPick.Host)
	}

	// Cleanup remaining
	releaseFirst()
	releaseThird()
}

func TestRoundRobinBalancerHealthChecks(t *testing.T) {
	banner("balancer_test.go")

	// Health endpoint responds 200 for healthy and 503 for unhealthy.
	healthyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	unhealthyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	serverUnhealthy := httptest.NewServer(unhealthyHandler)
	defer serverUnhealthy.Close()
	serverHealthy1 := httptest.NewServer(healthyHandler)
	defer serverHealthy1.Close()
	serverHealthy2 := httptest.NewServer(healthyHandler)
	defer serverHealthy2.Close()

	targets := []*url.URL{
		mustURL(t, serverUnhealthy.URL),
		mustURL(t, serverHealthy1.URL),
		mustURL(t, serverHealthy2.URL),
	}
	rrHealthBalancer := proxy.NewRoundRobinBalancer(targets, true)

	// Track that both healthy backends are actually chosen.
	unhealthyHost := mustURL(t, serverUnhealthy.URL).Host
	healthyHost1 := mustURL(t, serverHealthy1.URL).Host
	healthyHost2 := mustURL(t, serverHealthy2.URL).Host

	observedHealthy1 := false
	observedHealthy2 := false
	for i := 0; i < 6; i++ {
		pickedTarget := rrHealthBalancer.Pick(false)
		if pickedTarget == nil {
			t.Fatalf("expected a healthy target, got nil")
		}
		rrHealthBalancer.Acquire(pickedTarget)() // no-op

		switch pickedTarget.Host {
		case unhealthyHost:
			t.Fatalf("picked unhealthy target %s", pickedTarget.Host)
		case healthyHost1:
			observedHealthy1 = true
		case healthyHost2:
			observedHealthy2 = true
		}
	}
	if !observedHealthy1 || !observedHealthy2 {
		t.Fatalf("expected both healthy targets to be selected at least once; observedHealthy1=%v observedHealthy2=%v", observedHealthy1, observedHealthy2)
	}
}

func TestRoundRobinBalancerHealthAllUnhealthy(t *testing.T) {
	banner("balancer_test.go")

	unhealthyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	serverUnhealthy1 := httptest.NewServer(unhealthyHandler)
	defer serverUnhealthy1.Close()
	serverUnhealthy2 := httptest.NewServer(unhealthyHandler)
	defer serverUnhealthy2.Close()

	targets := []*url.URL{
		mustURL(t, serverUnhealthy1.URL),
		mustURL(t, serverUnhealthy2.URL),
	}
	rrHealthBalancer := proxy.NewRoundRobinBalancer(targets, true)

	// With all backends unhealthy, Pick should return nil.
	pickedTarget := rrHealthBalancer.Pick(false)
	if pickedTarget != nil {
		t.Fatalf("expected nil when all targets unhealthy, got %v", pickedTarget)
	}
}
