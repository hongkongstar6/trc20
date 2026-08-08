// Package merchant owns the merchant directory and the merchant signature
// scheme. Every user belongs to exactly one merchant: deposit addresses are
// allocated per (merchant, uid) and confirmed deposits are notified back to the
// merchant's own callback URL, signed with the merchant's own sha256 secret.
package merchant

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/store"
)

// SignField is the request field carrying the signature; it is never signed.
const SignField = "sign"

var (
	ErrNotFound  = errors.New("merchant not found")
	ErrDisabled  = errors.New("merchant disabled")
	ErrMissingID = errors.New("merchant_id is required")
	ErrBadSign   = errors.New("bad sign")
)

// Get loads a merchant by its business id.
func Get(ctx context.Context, merchantID string) (*model.Merchant, error) {
	if merchantID == "" {
		return nil, ErrMissingID
	}
	var row model.Merchant
	err := store.MyStore.DB.WithContext(ctx).Where("merchant_id = ?", merchantID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetEnabled loads a merchant and rejects it when the merchant is switched off.
func GetEnabled(ctx context.Context, merchantID string) (*model.Merchant, error) {
	row, err := Get(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	if row.Status != model.MerchantStatusOn {
		return nil, ErrDisabled
	}
	return row, nil
}

// Account is the unique wallet account: the merchant id plus the merchant's own
// user id. Two merchants may use the same uid without colliding.
func Account(merchantID, uid string) string {
	return merchantID + "_" + uid
}

// Sign implements the agreed scheme: sort the parameters by key ascending,
// join them as "k1=v1&k2=v2", append the merchant secret, and sha256 the
// result. The sign field itself and empty values are excluded.
//
//	params {"a":1,"b":2}, secret "sfejo" -> sha256("a=1&b=2sfejo")
func Sign(params map[string]any, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == SignField {
			continue
		}
		if _, ok := valueString(params[k]); !ok {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		v, _ := valueString(params[k])
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
	}
	b.WriteString(secret)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Verify recomputes the signature of params and compares it with params["sign"].
func Verify(params map[string]any, secret string) error {
	got, _ := params[SignField].(string)
	if got == "" {
		return ErrBadSign
	}
	want := Sign(params, secret)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(got)), []byte(want)) != 1 {
		return ErrBadSign
	}
	return nil
}

// DecodeParams decodes a JSON object into signable parameters. Numbers keep
// their original textual form so the signature matches what the caller sent.
func DecodeParams(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	params := map[string]any{}
	if err := dec.Decode(&params); err != nil {
		return nil, err
	}
	return params, nil
}

// String reads a string parameter, accepting numbers so a numeric uid signed as
// 1001 is usable as "1001".
func String(params map[string]any, key string) string {
	v, ok := valueString(params[key])
	if !ok {
		return ""
	}
	return v
}

func valueString(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		if t == "" {
			return "", false
		}
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	default:
		blob, err := json.Marshal(t)
		if err != nil {
			return "", false
		}
		s := string(blob)
		if s == "" || s == "null" {
			return "", false
		}
		return s, true
	}
}

// SignedPayload returns payload plus its signature, ready to be POSTed to a
// merchant callback URL.
func SignedPayload(payload map[string]any, secret string) ([]byte, error) {
	out := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		if k == SignField {
			continue
		}
		out[k] = v
	}
	out[SignField] = Sign(out, secret)
	blob, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("merchant: marshal payload: %w", err)
	}
	return blob, nil
}
