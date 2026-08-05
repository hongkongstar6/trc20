// Package tronenergyrent implements the tronenergyrent.com provider.
//
// Integration notes that the code depends on:
//   - every call is a GET with the api key in the query string, so the URL is
//     never logged and never returned in an error;
//   - the response envelope is {status,errorCode,errorDescription,requestId,payload};
//   - minimum energy order is 15000 units and periods are 1h/1d/3d/30d;
//   - the total price must be taken from the API: orders below 55000 energy
//     carry a surcharge, so unit price times amount is wrong;
//   - order placement has no idempotency key, therefore the caller writes its
//     local order row *before* calling Ensure and reconciles by order id
//     instead of blindly retrying;
//   - there is no callback, so delegation is confirmed by polling.
package tronenergyrent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
)

const Name = "tronenergyrent"

// Provider order states.
const (
	statePaid       = "PAID_BY_USER"
	stateWaiting    = "WAITING_DELEGATION"
	stateDelegated  = "ENERGY_DELEGATED"
	stateErrDelegat = "ERROR_DELEGATION"
	stateCancelled  = "CANCELLED"
)

func init() {
	energy.Register(Name, func(conf config.ProviderConf) (energy.Provider, error) {
		apiKey := energy.Option(conf, "api_key", "")
		if apiKey == "" {
			return nil, errors.New("tronenergyrent: api_key is required")
		}
		timeout, _ := time.ParseDuration(energy.Option(conf, "timeout", "10s"))
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		return &Provider{
			baseURL:     strings.TrimRight(energy.Option(conf, "base_url", "https://api.tronenergyrent.com"), "/"),
			apiKey:      apiKey,
			preActivate: energy.Option(conf, "pre_activate", "0") != "0",
			http:        &http.Client{Timeout: timeout},
		}, nil
	})
}

type Provider struct {
	baseURL     string
	apiKey      string
	preActivate bool
	http        *http.Client
}

func (p *Provider) Name() string { return Name }

type envelope struct {
	Status           string          `json:"status"`
	ErrorCode        string          `json:"errorCode"`
	ErrorDescription string          `json:"errorDescription"`
	RequestID        string          `json:"requestId"`
	Payload          json.RawMessage `json:"payload"`
}

// APIError exposes the provider error code (INVALID_API_KEY,
// INVALID_ENERGY_AMOUNT, NOT_ENOUGH_BALANCE, ORDER_NOT_FOUND, ...).
type APIError struct {
	Code        string
	Description string
	Path        string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("tronenergyrent: %s returned %s: %s", e.Path, e.Code, e.Description)
}

func (e *APIError) NotFound() bool { return e.Code == "ORDER_NOT_FOUND" }

func (p *Provider) get(ctx context.Context, path string, params url.Values, withKey bool, out any) error {
	if withKey {
		params.Set("apiKey", p.apiKey)
	}
	endpoint := p.baseURL + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		// The key lives in the query string, so the URL must stay out of logs.
		return fmt.Errorf("tronenergyrent: call %s failed", path)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("tronenergyrent: decode %s: %w", path, err)
	}
	if env.Status != "SUCCESS" {
		return &APIError{Code: env.ErrorCode, Description: env.ErrorDescription, Path: path}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Payload, out)
}

func (p *Provider) preActivateValue() string {
	if p.preActivate {
		return "1"
	}
	return "0"
}

func normalisePeriod(period string) string {
	switch period {
	case "1h", "1d", "3d", "30d":
		return period
	case "10m":
		return "1h" // shortest supported period
	default:
		return "1h"
	}
}

// --------------------------------------------------------------------- quotes

type priceData struct {
	AvailableEnergy    int64   `json:"availableEnergy"`
	TotalEnergy        int64   `json:"totalEnergy"`
	MinimumOrderEnergy int64   `json:"minimumOrderEnergy"`
	MaximumOrderEnergy int64   `json:"maximumOrderEnergy"`
	AvailableBandwidth int64   `json:"availableBandwidth"`
	MinimumOrderBand   int64   `json:"minimumOrderBandwidth"`
	MaximumOrderBand   int64   `json:"maximumOrderBandwidth"`
	TotalPriceSun      int64   `json:"totalPriceSun"`
	TotalPriceTrx      float64 `json:"totalPriceTrx"`
	Explanation        string  `json:"explanation"`
}

func (p *Provider) Quote(ctx context.Context, req energy.QuoteRequest) (*energy.Quote, error) {
	period := normalisePeriod(req.Period)
	params := url.Values{}
	params.Set("period", period)
	params.Set("preActivateDestinationAddress", p.preActivateValue())

	path := "/calculate-energy-price"
	amount := req.Amount
	if req.Resource == energy.ResourceBandwidth {
		path = "/calculate-bandwidth-price"
		params.Set("bandwidthAmount", itoa(amount))
	} else {
		params.Set("energyAmount", itoa(amount))
	}
	var data priceData
	if err := p.get(ctx, path, params, false, &data); err != nil {
		// Retry once at the provider minimum: an order below the minimum is
		// rejected outright, but we are still willing to pay the minimum.
		var apiErr *APIError
		if errors.As(err, &apiErr) && strings.Contains(apiErr.Code, "INVALID") {
			return p.quoteAtMinimum(ctx, req, path, params)
		}
		return nil, err
	}
	if req.Resource != energy.ResourceBandwidth && data.MinimumOrderEnergy > amount {
		return p.quoteAtMinimum(ctx, req, path, params)
	}
	available := data.AvailableEnergy
	if req.Resource == energy.ResourceBandwidth {
		available = data.AvailableBandwidth
	}
	if available > 0 && available < amount {
		return nil, fmt.Errorf("tronenergyrent: only %d units available, need %d", available, amount)
	}
	return &energy.Quote{
		Provider:    Name,
		CostTRX:     data.TotalPriceTrx,
		BilledUnits: amount,
		Available:   available,
		Period:      period,
	}, nil
}

func (p *Provider) quoteAtMinimum(ctx context.Context, req energy.QuoteRequest, path string, params url.Values) (*energy.Quote, error) {
	const minEnergy = 15000
	const minBandwidth = 1000
	minimum := int64(minEnergy)
	key := "energyAmount"
	if req.Resource == energy.ResourceBandwidth {
		minimum, key = minBandwidth, "bandwidthAmount"
	}
	if req.Amount >= minimum {
		return nil, fmt.Errorf("tronenergyrent: cannot quote %d units", req.Amount)
	}
	params.Set(key, itoa(minimum))
	var data priceData
	if err := p.get(ctx, path, params, false, &data); err != nil {
		return nil, err
	}
	return &energy.Quote{
		Provider:    Name,
		CostTRX:     data.TotalPriceTrx,
		BilledUnits: minimum,
		Period:      normalisePeriod(req.Period),
	}, nil
}

// --------------------------------------------------------------------- orders

type orderData struct {
	OrderID       string  `json:"orderId"`
	TotalPriceSun int64   `json:"totalPriceSun"`
	TotalPriceTrx float64 `json:"totalPriceTrx"`
	State         string  `json:"state"`
}

func (p *Provider) Ensure(ctx context.Context, req energy.OrderRequest) (*energy.Order, error) {
	period := normalisePeriod(req.Period)
	params := url.Values{}
	params.Set("period", period)
	params.Set("destinationAddress", req.Receiver)
	params.Set("preActivateDestinationAddress", p.preActivateValue())

	path := "/place-energy-order"
	if req.Resource == energy.ResourceBandwidth {
		path = "/place-bandwidth-order"
		params.Set("bandwidthAmount", itoa(req.Amount))
	} else {
		params.Set("energyAmount", itoa(req.Amount))
	}
	var data orderData
	if err := p.get(ctx, path, params, true, &data); err != nil {
		return nil, err
	}
	return &energy.Order{
		Provider:        Name,
		ProviderOrderID: data.OrderID,
		RequestID:       req.IdempotencyKey,
		State:           mapState(data.State),
		ProviderState:   data.State,
		CostTRX:         data.TotalPriceTrx,
	}, nil
}

type orderDetails struct {
	OrderID               string  `json:"orderId"`
	State                 string  `json:"state"`
	Period                string  `json:"period"`
	EnergyRequestedAmount int64   `json:"energyRequestedAmount"`
	EnergyDelegatedAmount int64   `json:"energyDelegatedAmount"`
	TotalPaidTrx          float64 `json:"totalPaidTrx"`
	RefundedTrx           float64 `json:"refundedTrx"`
	Transactions          []struct {
		TransactionHash string `json:"transactionHash"`
	} `json:"transactions"`
}

// Poll takes the *provider* order id: this platform has no request id of ours,
// which is exactly why callers persist the order id before waiting on it.
func (p *Provider) Poll(ctx context.Context, providerOrderID string) (*energy.Order, error) {
	params := url.Values{}
	params.Set("orderId", providerOrderID)
	var data orderDetails
	if err := p.get(ctx, "/single-order-details", params, true, &data); err != nil {
		return nil, err
	}
	order := &energy.Order{
		Provider:        Name,
		ProviderOrderID: data.OrderID,
		State:           mapState(data.State),
		ProviderState:   data.State,
		DelegatedEnergy: data.EnergyDelegatedAmount,
		CostTRX:         data.TotalPaidTrx - data.RefundedTrx,
	}
	if len(data.Transactions) > 0 {
		order.DelegateTxID = data.Transactions[0].TransactionHash
	}
	return order, nil
}

func mapState(state string) string {
	switch state {
	case stateDelegated:
		return energy.StateDelegated
	case stateErrDelegat:
		return energy.StateFailed
	case stateCancelled:
		return energy.StateCancelled
	case statePaid, stateWaiting:
		return energy.StatePending
	default:
		return energy.StatePending
	}
}

// -------------------------------------------------------------------- balance

type accountInfo struct {
	Email          string  `json:"email"`
	DepositAddress string  `json:"depositAddress"`
	BalanceSun     int64   `json:"balanceSun"`
	BalanceTrx     float64 `json:"balanceTrx"`
}

func (p *Provider) Balance(ctx context.Context) (float64, string, error) {
	var info accountInfo
	if err := p.get(ctx, "/account-info", url.Values{}, true, &info); err != nil {
		return 0, "", err
	}
	return info.BalanceTrx, info.DepositAddress, nil
}

func itoa(v int64) string { return fmt.Sprintf("%d", v) }
