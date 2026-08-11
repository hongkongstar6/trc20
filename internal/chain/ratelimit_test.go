package chain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
)

func TestSuspendedSecondsReadsTronGridBody(t *testing.T) {
	body := `{"Error":"The key exceeds the frequency limit(15), and the query server is suspended for 29 s"}`
	secs, ok := suspendedSeconds(body)
	if !ok || secs != 29 {
		t.Fatalf("suspendedSeconds = %d, %v, want 29, true", secs, ok)
	}
	if _, ok := suspendedSeconds(`{"Error":"other"}`); ok {
		t.Fatal("suspendedSeconds matched an unrelated body")
	}
}

func TestParseRetryAfterPrefersHeader(t *testing.T) {
	h := http.Header{"Retry-After": []string{"3"}}
	if d := parseRetryAfter(h, "suspended for 29 s"); d != 3*time.Second {
		t.Fatalf("d = %s, want 3s", d)
	}
	if d := parseRetryAfter(http.Header{}, "suspended for 29 s"); d != 29*time.Second {
		t.Fatalf("d = %s, want 29s", d)
	}
	if d := parseRetryAfter(http.Header{}, "boom"); d != defaultRetryAfter {
		t.Fatalf("d = %s, want %s", d, defaultRetryAfter)
	}
}

// The cooldown must never exceed the cap: a bogus Retry-After cannot be
// allowed to park the scanner.
func TestCooldownIsCapped(t *testing.T) {
	var c cooldown
	c.set(24 * time.Hour)
	if left := c.remaining(); left > maxRetryAfter {
		t.Fatalf("remaining = %s, want <= %s", left, maxRetryAfter)
	}
}

func TestLimiterPacesRequests(t *testing.T) {
	l := newLimiter(50, 1)
	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := l.wait(context.Background()); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}
	// 5 requests at 50 qps with a burst of 1 need at least 4 intervals.
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("elapsed = %s, want >= 60ms", elapsed)
	}
}

// A 429 from the primary must fail over immediately instead of surfacing as a
// node outage, and the throttled node must not be hit again while suspended.
func TestRateLimitedNodeFailsOverAndIsParked(t *testing.T) {
	var primaryHits, fallbackHits int32
	gw, closeAll := twoNodeGateway(t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&primaryHits, 1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"Error":"The key exceeds the frequency limit(15), and the query server is suspended for 29 s"}`))
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&fallbackHits, 1)
			_, _ = w.Write([]byte(`{"blockID":"aa","block_header":{"raw_data":{"number":42}}}`))
		}))
	defer closeAll()

	for i := 0; i < 3; i++ {
		if _, err := gw.GetNowBlock(context.Background()); err != nil {
			t.Fatalf("GetNowBlock: %v", err)
		}
	}
	if got := atomic.LoadInt32(&primaryHits); got != 1 {
		t.Fatalf("primaryHits = %d, want 1 (node must stay parked while suspended)", got)
	}
	if got := atomic.LoadInt32(&fallbackHits); got != 3 {
		t.Fatalf("fallbackHits = %d, want 3", got)
	}
}

// With a single node, a short suspension is waited out rather than reported as
// an outage: skipping a block would silently lose deposits.
func TestSingleNodeWaitsOutSuspension(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"Error":"suspended"}`))
			return
		}
		_, _ = w.Write([]byte(`{"blockID":"aa","block_header":{"raw_data":{"number":42}}}`))
	}))
	defer srv.Close()

	gw, err := NewGateway(config.ChainConfig{
		ChainNodes:    []config.NodeConfig{{Name: "only", Endpoint: srv.URL, Enabled: true, Timeout: "5s"}},
		RetryPerNode:  1,
		RateLimitWait: "10s",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	block, err := gw.GetNowBlock(context.Background())
	if err != nil {
		t.Fatalf("GetNowBlock: %v", err)
	}
	if block.Number() != 42 {
		t.Fatalf("number = %d, want 42", block.Number())
	}
}

// Waiting must stop when the caller gives up, so a suspended node cannot hold
// a shutdown hostage.
func TestSuspensionWaitRespectsRateLimitWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"Error":"suspended"}`))
	}))
	defer srv.Close()

	gw, err := NewGateway(config.ChainConfig{
		ChainNodes:    []config.NodeConfig{{Name: "only", Endpoint: srv.URL, Enabled: true, Timeout: "5s"}},
		RetryPerNode:  1,
		RateLimitWait: "1s",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	start := time.Now()
	if _, err := gw.GetNowBlock(context.Background()); err == nil {
		t.Fatal("GetNowBlock succeeded, want a rate limit error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("elapsed = %s, want the call to give up quickly", elapsed)
	}
}
