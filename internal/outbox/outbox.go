// Package outbox delivers wallet events to the business system. The wallet
// system owns no balances, so this stream is the only integration contract:
// at-least-once delivery with a stable event_id for downstream idempotency.
package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/sirupsen/logrus"
)

// Publisher is one delivery channel (HTTP callback, RocketMQ, ...).
type Publisher interface {
	Name() string
	Publish(ctx context.Context, event *model.NotifyOutbox) error
	Close() error
}

type Dispatcher struct {
	cfg config.NotifyConfig
	st  *store.Store
	//log        *logrus.Logger
	publishers []Publisher
}

func NewDispatcher(cfg config.NotifyConfig, st *store.Store, log *logrus.Logger, publishers ...Publisher) *Dispatcher {
	return &Dispatcher{cfg: cfg, st: st, publishers: publishers}
}

func (d *Dispatcher) Run(ctx context.Context) error {
	interval := config.Duration(d.cfg.PollInterval, 2*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		n, err := d.drainOnce(ctx)
		if err != nil {
			logrus.Error("outbox drain failed", "err", err)
		}
		if n > 0 {
			continue // keep draining while there is a backlog
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) drainOnce(ctx context.Context) (int, error) {
	var rows []model.NotifyOutbox
	err := d.st.DB.WithContext(ctx).
		Where("status = ? AND next_retry <= ?", model.OutboxStatePending, time.Now()).
		Order("id asc").Limit(d.cfg.BatchSize).Find(&rows).Error
	if err != nil {
		return 0, err
	}
	for i := range rows {
		d.deliver(ctx, &rows[i])
	}
	return len(rows), nil
}

func (d *Dispatcher) deliver(ctx context.Context, row *model.NotifyOutbox) {
	var lastErr error
	for _, p := range d.publishers {
		if err := p.Publish(ctx, row); err != nil {
			lastErr = fmt.Errorf("%s: %w", p.Name(), err)
			break
		}
	}
	now := time.Now()
	if lastErr == nil {
		d.st.DB.WithContext(ctx).Model(&model.NotifyOutbox{}).Where("id = ?", row.ID).
			UpdateColumns(map[string]any{
				"status": model.OutboxStateSent, "sent_at": now, "updated_at": now, "last_error": "",
			})
		return
	}
	retry := row.RetryCount + 1
	status := model.OutboxStatePending
	if retry >= d.cfg.MaxRetry {
		// Dead lettered events stay in the table and are exposed through the
		// reconciliation API, so nothing is ever silently dropped.
		status = model.OutboxStateDead
		logrus.Error("outbox event dead lettered", "event_id", row.EventID, "err", lastErr)
	}
	d.st.DB.WithContext(ctx).Model(&model.NotifyOutbox{}).Where("id = ?", row.ID).
		UpdateColumns(map[string]any{
			"status":      status,
			"retry_count": retry,
			"next_retry":  now.Add(backoff(retry)),
			"last_error":  truncate(lastErr.Error(), 240),
			"updated_at":  now,
		})
}

// maxBackoff caps the retry delay so a recovering business system is retried
// promptly instead of being parked for hours.
const maxBackoff = 10 * time.Minute

func backoff(retry int) time.Duration {
	if retry < 0 {
		retry = 0
	}
	// math.Pow overflows into +Inf for large retry counts, which converts to a
	// negative Duration, so the exponent is clamped before the multiplication.
	if retry > 20 {
		return maxBackoff
	}
	d := time.Duration(math.Pow(2, float64(retry))) * time.Second
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ------------------------------------------------------------ http publisher

type HTTPPublisher struct {
	url    string
	secret string
	client *http.Client
}

func NewHTTPPublisher(cfg config.NotifyConfig) *HTTPPublisher {
	return &HTTPPublisher{
		url:    cfg.HTTP.URL,
		secret: cfg.HTTP.Secret,
		client: &http.Client{Timeout: config.Duration(cfg.HTTP.Timeout, 10*time.Second)},
	}
}

func (p *HTTPPublisher) Name() string { return "http" }

func (p *HTTPPublisher) Publish(ctx context.Context, event *model.NotifyOutbox) error {
	body := []byte(event.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Id", event.EventID)
	req.Header.Set("X-Event-Type", event.EventType)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", Sign(p.secret, ts, body))
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(raw), 120))
	}
	return nil
}

func (p *HTTPPublisher) Close() error { return nil }

// Sign is the HMAC used both for outgoing callbacks and incoming API calls.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
