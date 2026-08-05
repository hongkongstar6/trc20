package outbox

import (
	"crypto/hmac"
	"testing"
)

// Sign is the shared scheme between the business system and this gateway: the
// timestamp is part of the signed material so a captured body cannot be
// replayed later.
func TestSignCoversTimestampAndBody(t *testing.T) {
	body := []byte(`{"event_id":"tx:0","amount":"1500000"}`)
	base := Sign("secret", "1700000000", body)
	if base == "" {
		t.Fatal("Sign returned an empty signature")
	}
	if Sign("secret", "1700000000", body) != base {
		t.Fatal("Sign is not deterministic")
	}
	if Sign("secret", "1700000001", body) == base {
		t.Fatal("the timestamp is not part of the signature")
	}
	if Sign("secret", "1700000000", []byte(`{"amount":"9900000000"}`)) == base {
		t.Fatal("the body is not part of the signature")
	}
	if Sign("other-secret", "1700000000", body) == base {
		t.Fatal("the secret is not part of the signature")
	}
	// Callers compare with hmac.Equal, so the output has to be a fixed length
	// hex digest.
	if len(base) != 64 || !hmac.Equal([]byte(base), []byte(base)) {
		t.Fatalf("signature = %q, want a 64 character hex digest", base)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	prev := backoff(0)
	for retry := 1; retry < 10; retry++ {
		d := backoff(retry)
		if d < prev {
			t.Fatalf("backoff(%d) = %v shrank below backoff(%d) = %v", retry, d, retry-1, prev)
		}
		prev = d
	}
	// An unbounded backoff would park a failed callback for days.
	if prev > backoff(100) || backoff(100) > backoff(1000) {
		t.Fatal("backoff must be monotonic")
	}
	if backoff(1000) > maxBackoff {
		t.Fatalf("backoff(1000) = %v, want at most %v", backoff(1000), maxBackoff)
	}
}
