package tronenergyrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
)

const testKey = "test-api-key"

func newTestProvider(t *testing.T, handler http.Handler) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	provs, err := energy.Build(config.EnergyConfig{Providers: map[string]config.ProviderConf{
		Name: {Enabled: true, Options: map[string]string{"base_url": srv.URL, "api_key": testKey}},
	}})
	if err != nil {
		srv.Close()
		t.Fatalf("Build: %v", err)
	}
	return provs[Name].(*Provider), srv
}

// The total price has to come from the API: orders below the platform's sweet
// spot carry a surcharge, so unit price times amount understates the cost.
func TestQuoteUsesProviderTotalPrice(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("energyAmount"); got != "65000" {
			t.Errorf("energyAmount = %q", got)
		}
		_, _ = w.Write([]byte(`{"status":"SUCCESS","payload":{"availableEnergy":75820891,"minimumOrderEnergy":15000,
			"maximumOrderEnergy":200000000,"totalPriceSun":2860000,"totalPriceTrx":2.86}}`))
	}))
	defer srv.Close()

	q, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 65000, Period: "1h"})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if q.CostTRX != 2.86 {
		t.Fatalf("cost = %f, want the provider reported 2.86", q.CostTRX)
	}
}

// The api key travels in the query string, so it must be attached to
// authenticated calls only and never to public pricing calls.
func TestQuoteDoesNotLeakAPIKey(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apiKey") != "" {
			t.Error("the public pricing endpoint must not receive the api key")
		}
		_, _ = w.Write([]byte(`{"status":"SUCCESS","payload":{"availableEnergy":1000000,"minimumOrderEnergy":15000,"totalPriceTrx":1.0}}`))
	}))
	defer srv.Close()

	if _, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 65000, Period: "1h"}); err != nil {
		t.Fatal(err)
	}
}

func TestQuoteRetriesAtMinimumOrder(t *testing.T) {
	var amounts []string
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		amounts = append(amounts, r.URL.Query().Get("energyAmount"))
		_, _ = w.Write([]byte(`{"status":"SUCCESS","payload":{"availableEnergy":1000000,"minimumOrderEnergy":15000,"totalPriceTrx":0.66}}`))
	}))
	defer srv.Close()

	q, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 5000, Period: "1h"})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if q.BilledUnits != 15000 {
		t.Fatalf("billed = %d, want the 15000 minimum", q.BilledUnits)
	}
	if len(amounts) != 2 || amounts[1] != "15000" {
		t.Fatalf("requested amounts = %v, want a retry at the minimum", amounts)
	}
}

func TestQuoteFailsWhenStockIsShort(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"SUCCESS","payload":{"availableEnergy":1000,"minimumOrderEnergy":15000,"totalPriceTrx":1.0}}`))
	}))
	defer srv.Close()

	if _, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 65000, Period: "1h"}); err == nil {
		t.Fatal("insufficient stock must remove the provider from the comparison")
	}
}

func TestPlaceOrderSendsAPIKeyAndPeriod(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apiKey") != testKey {
			t.Errorf("apiKey = %q", q.Get("apiKey"))
		}
		if q.Get("period") != "1h" || q.Get("destinationAddress") == "" {
			t.Errorf("unexpected query %v", q)
		}
		_, _ = w.Write([]byte(`{"status":"SUCCESS","payload":{"orderId":"o-1","totalPriceTrx":5.66,"state":"PAID_BY_USER"}}`))
	}))
	defer srv.Close()

	order, err := p.Ensure(context.Background(), energy.OrderRequest{
		Resource: energy.ResourceEnergy, Amount: 65000, Period: "1h",
		Receiver: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", IdempotencyKey: "local-1",
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// The provider has no idempotency key, so the local request id is kept on
	// the order row and the provider order id is what we poll.
	if order.ProviderOrderID != "o-1" || order.RequestID != "local-1" || order.State != energy.StatePending {
		t.Fatalf("unexpected order %+v", order)
	}
}

func TestPollMapsProviderStates(t *testing.T) {
	cases := map[string]string{
		statePaid:       energy.StatePending,
		stateWaiting:    energy.StatePending,
		stateDelegated:  energy.StateDelegated,
		stateErrDelegat: energy.StateFailed,
		stateCancelled:  energy.StateCancelled,
	}
	for provState, want := range cases {
		provState, want := provState, want
		p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"status":"SUCCESS","payload":{"orderId":"o-1","state":"` + provState +
				`","energyDelegatedAmount":128708,"totalPaidTrx":5.6628,"refundedTrx":0,
				"transactions":[{"transactionHash":"98c6"}]}}`))
		}))
		order, err := p.Poll(context.Background(), "o-1")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", provState, err)
		}
		if order.State != want {
			t.Fatalf("%s mapped to %s, want %s", provState, order.State, want)
		}
	}
}

func TestPollReportsNetCostAfterRefund(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"SUCCESS","payload":{"orderId":"o-1","state":"ENERGY_DELEGATED",
			"energyDelegatedAmount":100000,"totalPaidTrx":5.0,"refundedTrx":1.5}}`))
	}))
	defer srv.Close()

	order, err := p.Poll(context.Background(), "o-1")
	if err != nil {
		t.Fatal(err)
	}
	if order.CostTRX < 3.49 || order.CostTRX > 3.51 {
		t.Fatalf("cost = %f, want 3.5 net of the refund", order.CostTRX)
	}
}

func TestErrorEnvelopeBecomesAPIError(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ERROR","errorCode":"ORDER_NOT_FOUND","errorDescription":"Order not found","payload":null}`))
	}))
	defer srv.Close()

	_, err := p.Poll(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || !apiErr.NotFound() {
		t.Fatalf("err = %v, want ORDER_NOT_FOUND", err)
	}
}

func TestBalanceReturnsDepositAddress(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"SUCCESS","payload":{"email":"user@example.com",
			"depositAddress":"TVKpzEiJvhxriRDNoSucoXgMmu49RmXagU","balanceSun":680910276,"balanceTrx":680.910276}}`))
	}))
	defer srv.Close()

	balance, deposit, err := p.Balance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if balance < 680.9 || balance > 680.92 || deposit != "TVKpzEiJvhxriRDNoSucoXgMmu49RmXagU" {
		t.Fatalf("balance = %f, deposit = %s", balance, deposit)
	}
}

func TestNormalisePeriod(t *testing.T) {
	for input, want := range map[string]string{"1h": "1h", "1d": "1d", "3d": "3d", "30d": "30d", "10m": "1h", "": "1h", "bogus": "1h"} {
		if got := normalisePeriod(input); got != want {
			t.Fatalf("normalisePeriod(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildRequiresAPIKey(t *testing.T) {
	if _, err := energy.Build(config.EnergyConfig{Providers: map[string]config.ProviderConf{
		Name: {Enabled: true},
	}}); err == nil {
		t.Fatal("a missing api key must fail at startup")
	}
}
