// Package gasstation implements the GasStation (gasstation.ai) energy rental
// provider.
//
// Integration notes that the code depends on:
//   - every request carries app_id in the query string plus a `data` parameter
//     holding the AES-128-ECB / PKCS7 / base64 encrypted JSON payload;
//   - code == 0 means success;
//   - the rental period is a "service_charge_type" code (10010 = 10 min,
//     20001 = 1 hour, 30001 = 1 day);
//   - minimum energy purchase is 64000+ units, so small sweeps are billed at
//     the minimum: quotes must use the provider reported total price;
//   - orders are asynchronous; status 1 = delegated, 2 = failed, 3 = partial;
//   - the API requires a fixed outbound IP allowlist (error 100006).
package gasstation

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
)

const Name = "gasstation"

const (
	statusCreated   = 0
	statusDelegated = 1
	statusFailed    = 2
	statusPartial   = 3
	statusReclaimed = 10
)

func init() {
	f := func(conf config.ProviderConf) (energy.Provider, error) {
		appID := energy.Option(conf, "app_id", "")
		secret := energy.Option(conf, "app_secret", "")
		if appID == "" || secret == "" {
			return nil, errors.New("gasstation: app_id and app_secret are required")
		}
		if len(secret) != 16 && len(secret) != 24 && len(secret) != 32 {
			return nil, fmt.Errorf("gasstation: app_secret must be 16/24/32 bytes for AES, got %d", len(secret))
		}
		timeout, _ := time.ParseDuration(energy.Option(conf, "timeout", "10s"))
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		return &Provider{
			baseURL: strings.TrimRight(energy.Option(conf, "base_url", "https://openapi.gasstation.ai"), "/"),
			appID:   appID,
			secret:  []byte(secret),
			http:    &http.Client{Timeout: timeout},
			periods: map[string]string{
				"10m": energy.Option(conf, "period_10m", "10010"),
				"1h":  energy.Option(conf, "period_1h", "20001"),
				"1d":  energy.Option(conf, "period_1d", "30001"),
			},
		}, nil
	}

	energy.Register(Name, f)
}

type Provider struct {
	baseURL string
	appID   string
	secret  []byte
	http    *http.Client
	periods map[string]string
}

func (p *Provider) Name() string { return Name }

// ------------------------------------------------------------------ transport

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// encrypt implements AES-ECB with PKCS7 padding, which is what the GasStation
// `data` query parameter expects.
func (p *Provider) encrypt(payload any) (string, error) {
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(p.secret)
	if err != nil {
		return "", err
	}
	size := block.BlockSize()
	pad := size - len(plain)%size
	padded := append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += size {
		block.Encrypt(out[i:i+size], padded[i:i+size])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func (p *Provider) request(ctx context.Context, method, path string, payload any, out any) error {
	data, err := p.encrypt(payload)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("app_id", p.appID)
	q.Set("data", data)
	endpoint := p.baseURL + path + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		// Never log the URL: it carries app_id and the encrypted payload.
		return fmt.Errorf("gasstation: call %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("gasstation: decode %s: %w", path, err)
	}
	if env.Code != 0 {
		return &APIError{Code: env.Code, Msg: env.Msg, Path: path}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// APIError carries the provider error code so callers can react to the ones
// that matter: 100006 invalid IP, 110034 decryption failure, 110042 out of
// energy, 110044 duplicate transaction.
type APIError struct {
	Code int
	Msg  string
	Path string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gasstation: %s returned code %d: %s", e.Path, e.Code, e.Msg)
}

func (e *APIError) OutOfStock() bool { return e.Code == 110042 }
func (e *APIError) Duplicate() bool  { return e.Code == 110044 }

// --------------------------------------------------------------------- quotes

type pricePayload struct {
	ResourceType      string `json:"resource_type"`
	ServiceChargeType string `json:"service_charge_type,omitempty"`
}

type priceData struct {
	ResourceType string `json:"resource_type"`
	MinNumber    int64  `json:"min_number"`
	MaxNumber    int64  `json:"max_number"`
	PriceList    []struct {
		ExpireMin         string `json:"expire_min"`
		ServiceChargeType string `json:"service_charge_type"`
		Price             string `json:"price"`
		RemainingNumber   string `json:"remaining_number"`
	} `json:"price_builder_list"`
}

func (p *Provider) periodCode(period string) string {
	if code, ok := p.periods[period]; ok && code != "" {
		return code
	}
	return p.periods["1h"]
}

func (p *Provider) Quote(ctx context.Context, req energy.QuoteRequest) (*energy.Quote, error) {
	resource := "energy"
	if req.Resource == energy.ResourceBandwidth {
		resource = "net"
	}
	code := p.periodCode(req.Period)
	var data priceData
	if err := p.request(ctx, http.MethodGet, "/api/tron/gas/order/price",
		pricePayload{ResourceType: resource, ServiceChargeType: code}, &data); err != nil {
		return nil, err
	}
	billed := req.Amount
	if data.MinNumber > billed {
		// Below the minimum we still pay for the minimum; the comparison
		// engine must see the real cost, not the theoretical one.
		billed = data.MinNumber
	}
	if data.MaxNumber > 0 && billed > data.MaxNumber {
		return nil, fmt.Errorf("gasstation: %d units exceeds the max order size %d", billed, data.MaxNumber)
	}
	for _, entry := range data.PriceList {
		if entry.ServiceChargeType != code {
			continue
		}
		unitSun, err := strconv.ParseFloat(entry.Price, 64)
		if err != nil {
			return nil, fmt.Errorf("gasstation: bad price %q: %w", entry.Price, err)
		}
		remaining, _ := strconv.ParseInt(entry.RemainingNumber, 10, 64)
		if remaining > 0 && remaining < billed {
			return nil, fmt.Errorf("gasstation: only %d units in stock, need %d", remaining, billed)
		}
		return &energy.Quote{
			Provider:    Name,
			CostTRX:     unitSun * float64(billed) / 1e6,
			BilledUnits: billed,
			Available:   remaining,
			Period:      req.Period,
		}, nil
	}
	return nil, fmt.Errorf("gasstation: no price for period %s (code %s)", req.Period, code)
}

// --------------------------------------------------------------------- orders

type createOrderPayload struct {
	RequestID         string `json:"request_id"`
	ReceiveAddress    string `json:"receive_address"`
	BuyType           int    `json:"buy_type"`
	ServiceChargeType string `json:"service_charge_type"`
	EnergyNum         int64  `json:"energy_num,omitempty"`
	NetNum            int64  `json:"net_num,omitempty"`
}

type createOrderData struct {
	TradeNo string `json:"trade_no"`
}

func (p *Provider) Ensure(ctx context.Context, req energy.OrderRequest) (*energy.Order, error) {
	payload := createOrderPayload{
		RequestID:         req.IdempotencyKey,
		ReceiveAddress:    req.Receiver,
		BuyType:           0,
		ServiceChargeType: p.periodCode(req.Period),
	}
	if req.Resource == energy.ResourceBandwidth {
		payload.NetNum = req.Amount
	} else {
		payload.EnergyNum = req.Amount
	}
	var data createOrderData
	err := p.request(ctx, http.MethodPost, "/api/tron/gas/create_order", payload, &data)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Duplicate() {
			// The request id already exists: reconcile instead of reordering.
			return p.Poll(ctx, req.IdempotencyKey)
		}
		return nil, err
	}
	return &energy.Order{
		Provider:        Name,
		ProviderOrderID: data.TradeNo,
		RequestID:       req.IdempotencyKey,
		State:           energy.StatePending,
	}, nil
}

type recordItem struct {
	TradeNo           string `json:"trade_no"`
	RequestID         string `json:"request_id"`
	Status            int    `json:"status"`
	ReceiveAddress    string `json:"receive_address"`
	Amount            string `json:"amount"`
	DelegateEnergyNum int64  `json:"delegate_energy_num"`
	DelegateNetNum    int64  `json:"delegate_net_num"`
	ItemList          []struct {
		EnergyTxID string `json:"energy_txid"`
	} `json:"item_list"`
}

type recordPayload struct {
	RequestIDs string `json:"request_ids"`
}

func (p *Provider) Poll(ctx context.Context, requestID string) (*energy.Order, error) {
	var items []recordItem
	if err := p.request(ctx, http.MethodGet, "/api/tron/gas/record/list",
		recordPayload{RequestIDs: requestID}, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return &energy.Order{Provider: Name, RequestID: requestID, State: energy.StatePending}, nil
	}
	it := items[0]
	cost, _ := strconv.ParseFloat(it.Amount, 64)
	order := &energy.Order{
		Provider:        Name,
		ProviderOrderID: it.TradeNo,
		RequestID:       requestID,
		ProviderState:   strconv.Itoa(it.Status),
		DelegatedEnergy: it.DelegateEnergyNum,
		CostTRX:         cost,
	}
	if len(it.ItemList) > 0 {
		order.DelegateTxID = it.ItemList[0].EnergyTxID
	}
	switch it.Status {
	case statusDelegated, statusPartial, statusReclaimed:
		order.State = energy.StateDelegated
	case statusFailed:
		order.State = energy.StateFailed
	case statusCreated:
		order.State = energy.StatePending
	default:
		order.State = energy.StatePending
	}
	return order, nil
}

// -------------------------------------------------------------------- balance

type balancePayload struct {
	Time string `json:"time"`
}

type balanceData struct {
	Symbol         string `json:"symbol"`
	Balance        string `json:"balance"`
	DepositAddress string `json:"deposit_address"`
}

func (p *Provider) Balance(ctx context.Context) (float64, string, error) {
	var data balanceData
	payload := balancePayload{Time: strconv.FormatInt(time.Now().Unix(), 10)}
	if err := p.request(ctx, http.MethodGet, "/api/tron/gas/balance", payload, &data); err != nil {
		return 0, "", err
	}
	balance, err := strconv.ParseFloat(data.Balance, 64)
	if err != nil {
		return 0, data.DepositAddress, fmt.Errorf("gasstation: bad balance %q: %w", data.Balance, err)
	}
	return balance, data.DepositAddress, nil
}
