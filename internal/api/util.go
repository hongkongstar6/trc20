package api

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func newReader(b []byte) io.Reader { return bytes.NewReader(b) }

// parseRange reads from/to unix timestamps, defaulting to the last 24 hours.
func parseRange(c *gin.Context) (time.Time, time.Time, error) {
	now := time.Now()
	from, to := now.Add(-24*time.Hour), now
	if v := c.Query("from"); v != "" {
		unix, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return from, to, fmt.Errorf("invalid from")
		}
		from = time.Unix(unix, 0)
	}
	if v := c.Query("to"); v != "" {
		unix, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return from, to, fmt.Errorf("invalid to")
		}
		to = time.Unix(unix, 0)
	}
	if to.Before(from) {
		return from, to, fmt.Errorf("to must be after from")
	}
	return from, to, nil
}

func parseLimit(c *gin.Context, def, max int) int {
	v, err := strconv.Atoi(c.Query("limit"))
	if err != nil || v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// splitEventID parses the "<txid>:<event_index>" deposit event id.
func splitEventID(eventID string) (string, int, error) {
	parts := strings.Split(eventID, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("event_id must look like <txid>:<event_index>")
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("invalid event index")
	}
	return parts[0], index, nil
}
