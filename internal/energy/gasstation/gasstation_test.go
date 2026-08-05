package gasstation

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
)

const testSecret = "0123456789abcdef" // 16 bytes -> AES-128

func newTestProvider(t *testing.T, handler http.Handler) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	p, err := energy.Build(config.EnergyConfig{Providers: map[string]config.ProviderConf{
		Name: {Enabled: true, Options: map[string]string{
			"base_url":   srv.URL,
			"app_id":     "test-app",
			"app_secret": testSecret,
		}},
	}})
	if err != nil {
		srv.Close()
		t.Fatalf("Build: %v", err)
	}
	return p[Name].(*Provider), srv
}

// decryptRequest mirrors what the platform does server side, proving the
// AES-ECB/PKCS7/base64 encoding of the `data` parameter is interoperable.
func decryptRequest(t *testing.T, encoded string, out any) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	block, err := aes.NewCipher([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%block.BlockSize() != 0 || len(raw) == 0 {
		t.Fatalf("ciphertext length %d is not a block multiple", len(raw))
	}
	plain := make([]byte, len(raw))
	for i := 0; i < len(raw); i += block.BlockSize() {
		block.Decrypt(plain[i:i+block.BlockSize()], raw[i:i+block.BlockSize()])
	}
	pad := int(plain[len(plain)-1])
	if pad <= 0 || pad > block.BlockSize() {
		t.Fatalf("bad pkcs7 padding %d", pad)
	}
	if !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		t.Fatal("inconsistent pkcs7 padding")
	}
	if err := json.Unmarshal(plain[:len(plain)-pad], out); err != nil {
		t.Fatalf("payload is not the JSON we sent: %v", err)
	}
}

func TestRequestPayloadIsEncryptedAndSigned(t *testing.T) {
	var seen struct {
		ResourceType      string `json:"resource_type"`
		ServiceChargeType string `json:"service_charge_type"`
	}
	var appID string
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appID = r.URL.Query().Get("app_id")
		decryptRequest(t, r.URL.Query().Get("data"), &seen)
		_, _ = w.Write([]byte(`{"code":0,"msg":"Success","data":{"resource_type":"energy","min_number":64000,"max_number":10000000,
			"price_builder_list":[{"expire_min":"60","service_charge_type":"20001","price":"53","remaining_number":"5000000"}]}}`))
	}))
	defer srv.Close()

	if _, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 64000, Period: "1h"}); err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if appID != "test-app" {
		t.Fatalf("app_id = %q", appID)
	}
	if seen.ResourceType != "energy" || seen.ServiceChargeType != "20001" {
		t.Fatalf("decrypted payload = %+v", seen)
	}
}

// Below the platform minimum we still pay for the minimum, so the quote has to
// report the real cost or comparison shopping picks the wrong provider.
func TestQuoteBillsThePlatformMinimum(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"Success","data":{"resource_type":"energy","min_number":64000,"max_number":10000000,
			"price_builder_list":[{"expire_min":"60","service_charge_type":"20001","price":"53","remaining_number":"5000000"}]}}`))
	}))
	defer srv.Close()

	q, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 32000, Period: "1h"})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if q.BilledUnits != 64000 {
		t.Fatalf("billed = %d, want the 64000 platform minimum", q.BilledUnits)
	}
	// 64000 * 53 sun = 3,392,000 sun = 3.392 TRX
	if q.CostTRX < 3.39 || q.CostTRX > 3.40 {
		t.Fatalf("cost = %f TRX, want ~3.392", q.CostTRX)
	}
}

func TestQuoteFailsWhenStockIsShort(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"Success","data":{"resource_type":"energy","min_number":1000,"max_number":10000000,
			"price_builder_list":[{"expire_min":"60","service_charge_type":"20001","price":"53","remaining_number":"1000"}]}}`))
	}))
	defer srv.Close()

	if _, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 64000, Period: "1h"}); err == nil {
		t.Fatal("a provider without enough stock must drop out of the comparison")
	}
}

func TestAPIErrorIsSurfaced(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 100006 is the "IP not in the allowlist" error.
		_, _ = w.Write([]byte(`{"code":100006,"msg":"invalid ip","data":null}`))
	}))
	defer srv.Close()

	_, _, err := p.Balance(context.Background())
	var apiErr *APIError
	if err == nil {
		t.Fatal("expected an API error")
	}
	if !errorsAs(err, &apiErr) || apiErr.Code != 100006 {
		t.Fatalf("err = %v, want APIError{Code:100006}", err)
	}
}

// A duplicate request id means the order already exists; placing another one
// would pay twice for the same energy.
func TestEnsureReconcilesDuplicateRequestID(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tron/gas/create_order" {
			_, _ = w.Write([]byte(`{"code":110044,"msg":"duplicate transaction","data":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"Success","data":[{"trade_no":"T1","request_id":"req-1","status":1,
			"amount":"3.392","delegate_energy_num":64000,"item_list":[{"energy_txid":"abc"}]}]}`))
	}))
	defer srv.Close()

	order, err := p.Ensure(context.Background(), energy.OrderRequest{
		Resource: energy.ResourceEnergy, Amount: 64000, Period: "1h",
		Receiver: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", IdempotencyKey: "req-1",
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if order.State != energy.StateDelegated || order.ProviderOrderID != "T1" {
		t.Fatalf("unexpected order %+v", order)
	}
}

func TestPollMapsProviderStates(t *testing.T) {
	cases := map[int]string{
		0:  energy.StatePending,
		1:  energy.StateDelegated,
		2:  energy.StateFailed,
		3:  energy.StateDelegated, // partially delegated is still usable
		10: energy.StateDelegated,
	}
	for status, want := range cases {
		status, want := status, want
		p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":0,"msg":"Success","data":[{"trade_no":"T1","request_id":"req-1","status":` +
				itoa(status) + `,"amount":"1.0","delegate_energy_num":64000}]}`))
		}))
		order, err := p.Poll(context.Background(), "req-1")
		srv.Close()
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if order.State != want {
			t.Fatalf("status %d mapped to %s, want %s", status, order.State, want)
		}
	}
}

func TestBalanceReturnsDepositAddress(t *testing.T) {
	p, srv := newTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"Success","data":{"symbol":"trx","balance":"92.479205","deposit_address":"TVKpzEiJvhxriRDNoSucoXgMmu49RmXagU"}}`))
	}))
	defer srv.Close()

	balance, deposit, err := p.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if balance < 92.47 || balance > 92.48 {
		t.Fatalf("balance = %f", balance)
	}
	// The auto topup guard compares this against its hard coded whitelist.
	if deposit != "TVKpzEiJvhxriRDNoSucoXgMmu49RmXagU" {
		t.Fatalf("deposit address = %s", deposit)
	}
}

func TestBuildRejectsWrongSecretLength(t *testing.T) {
	_, err := energy.Build(config.EnergyConfig{Providers: map[string]config.ProviderConf{
		Name: {Enabled: true, Options: map[string]string{"app_id": "a", "app_secret": "short"}},
	}})
	if err == nil {
		t.Fatal("an AES key of the wrong length must fail at startup, not at the first order")
	}
}
