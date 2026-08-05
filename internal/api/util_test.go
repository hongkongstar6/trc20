package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func ctxWithQuery(query string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?"+query, nil)
	return c
}

func TestParseRangeDefaultsToLastDay(t *testing.T) {
	from, to, err := parseRange(ctxWithQuery(""))
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}
	if d := to.Sub(from); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("default window = %v, want about 24h", d)
	}
}

func TestParseRangeReadsUnixTimestamps(t *testing.T) {
	from, to, err := parseRange(ctxWithQuery("from=1700000000&to=1700003600"))
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}
	if from.Unix() != 1700000000 || to.Unix() != 1700003600 {
		t.Fatalf("from=%d to=%d", from.Unix(), to.Unix())
	}
}

func TestParseRangeRejectsBadInput(t *testing.T) {
	for _, q := range []string{"from=yesterday", "to=tomorrow", "from=1700003600&to=1700000000"} {
		if _, _, err := parseRange(ctxWithQuery(q)); err == nil {
			t.Fatalf("parseRange(%q) should have failed", q)
		}
	}
}

func TestParseLimitClamps(t *testing.T) {
	if got := parseLimit(ctxWithQuery(""), 100, 500); got != 100 {
		t.Fatalf("got %d, want the default", got)
	}
	if got := parseLimit(ctxWithQuery("limit=9999"), 100, 500); got != 500 {
		t.Fatalf("got %d, want the max", got)
	}
	if got := parseLimit(ctxWithQuery("limit=0"), 100, 500); got != 100 {
		t.Fatalf("got %d, want the default", got)
	}
	if got := parseLimit(ctxWithQuery("limit=250"), 100, 500); got != 250 {
		t.Fatalf("got %d", got)
	}
}

func TestSplitEventID(t *testing.T) {
	txid, index, err := splitEventID("abc123:4")
	if err != nil {
		t.Fatalf("splitEventID: %v", err)
	}
	if txid != "abc123" || index != 4 {
		t.Fatalf("txid=%s index=%d", txid, index)
	}
	for _, id := range []string{"abc123", "abc123:x", "abc:1:2", ""} {
		if _, _, err := splitEventID(id); err == nil {
			t.Fatalf("splitEventID(%q) should have failed", id)
		}
	}
}
