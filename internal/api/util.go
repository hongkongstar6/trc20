package api

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
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

// parseTokenAmount converts a withdrawal amount expressed in the token's own
// unit into the minimum units the chain transfers: with 6 decimals "11" is
// 11 USDT (11000000 units) and "0.5" is 500000 units. Everything downstream
// (the record, the transfer, the risk limits) keeps working in minimum units.
func parseTokenAmount(amount string, decimals int) (*big.Int, error) {
	if decimals < 0 {
		return nil, fmt.Errorf("token decimals must not be negative")
	}
	text := strings.TrimSpace(amount)
	whole, frac := text, ""
	if i := strings.IndexByte(text, '.'); i >= 0 {
		whole, frac = text[:i], text[i+1:]
	}
	if whole == "" {
		whole = "0"
	}
	if !isDigits(whole) || (frac != "" && !isDigits(frac)) {
		return nil, fmt.Errorf("amount must be a decimal number in token units")
	}
	if len(frac) > decimals {
		return nil, fmt.Errorf("amount has more than %d decimal places", decimals)
	}
	// Padding the fraction to the token precision turns the decimal amount into
	// minimum units without any float rounding.
	units, ok := new(big.Int).SetString(whole+frac+strings.Repeat("0", decimals-len(frac)), 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	if units.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return units, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
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
