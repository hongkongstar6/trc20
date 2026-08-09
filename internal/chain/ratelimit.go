package chain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// errRateLimited marks a node answer that must not be treated as a node
// outage: the node is healthy, the caller simply has to slow down.
var errRateLimited = errors.New("rate limited")

// rateLimitError reports a 429 answer.
type rateLimitError struct {
	body string
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("http %d: %s", http.StatusTooManyRequests, e.body)
}

func (e *rateLimitError) Unwrap() error { return errRateLimited }

// maxRetryAfter caps a provider supplied cooldown, so a bogus header cannot
// park the scanner for hours.
const maxRetryAfter = 2 * time.Minute

// defaultRetryAfter is used when the provider gives no hint at all.
const defaultRetryAfter = 5 * time.Second

// limiter is a token bucket shared by every request to one node. TronGrid
// counts requests per API key, so throttling has to happen before the request
// leaves the process: the scanner fetches blocks concurrently and would
// otherwise burst far above the key's QPS.
type limiter struct {
	mu     sync.Mutex
	qps    float64
	burst  float64
	tokens float64
	last   time.Time
}

func newLimiter(qps float64, burst int) *limiter {
	if qps <= 0 {
		return nil
	}
	b := float64(burst)
	if b < 1 {
		b = 1
	}
	return &limiter{qps: qps, burst: b, tokens: b, last: time.Now()}
}

// reserve consumes one token and reports how long the caller must sleep first.
func (l *limiter) reserve(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.last.IsZero() {
		l.tokens += now.Sub(l.last).Seconds() * l.qps
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
	}
	l.last = now
	l.tokens--
	if l.tokens >= 0 {
		return 0
	}
	// The token is already taken, so concurrent callers queue up instead of
	// all waiting for the same slot.
	return time.Duration(-l.tokens / l.qps * float64(time.Second))
}

func (l *limiter) wait(ctx context.Context) error {
	if l == nil {
		return ctx.Err()
	}
	d := l.reserve(time.Now())
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// cooldown remembers until when a node refuses traffic.
type cooldown struct {
	mu    sync.Mutex
	until time.Time
}

func (c *cooldown) set(d time.Duration) {
	if d <= 0 {
		d = defaultRetryAfter
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if until := time.Now().Add(d); until.After(c.until) {
		c.until = until
	}
}

// remaining is zero when the node may be used again.
func (c *cooldown) remaining() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Until(c.until)
}

// parseRetryAfter reads the standard header first and falls back to the hint
// TronGrid embeds in the body:
//
//	{"Error":"The key exceeds the frequency limit(15), and the query server
//	 is suspended for 29 s"}
func parseRetryAfter(h http.Header, body string) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	if secs, ok := suspendedSeconds(body); ok {
		return time.Duration(secs) * time.Second
	}
	return defaultRetryAfter
}

// suspendedSeconds extracts N from "... suspended for N s".
func suspendedSeconds(body string) (int, bool) {
	const marker = "suspended for "
	i := strings.Index(body, marker)
	if i < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(body[i+len(marker):])
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	secs, err := strconv.Atoi(rest[:end])
	if err != nil || secs <= 0 {
		return 0, false
	}
	return secs, true
}
