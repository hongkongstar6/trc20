package energy

import (
	"context"
	"errors"
	"testing"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/sirupsen/logrus"
)

type fakeProvider struct {
	name    string
	cost    float64
	billed  int64
	err     error
	quotes  int
	deposit string
	balance float64
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Quote(context.Context, QuoteRequest) (*Quote, error) {
	f.quotes++
	if f.err != nil {
		return nil, f.err
	}
	return &Quote{Provider: f.name, CostTRX: f.cost, BilledUnits: f.billed, Period: "1h"}, nil
}

func (f *fakeProvider) Ensure(context.Context, OrderRequest) (*Order, error) {
	return &Order{Provider: f.name, State: StatePending}, nil
}

func (f *fakeProvider) Poll(context.Context, string) (*Order, error) {
	return &Order{Provider: f.name, State: StateDelegated}, nil
}

func (f *fakeProvider) Balance(context.Context) (float64, string, error) {
	return f.balance, f.deposit, nil
}

func testLogger() *logrus.Logger {
	return logrus.New()
}

func newTestManager(mode string, provs map[string]Provider) *Manager {
	cfg := config.EnergyConfig{Mode: mode, DefaultPeriod: "1h", QuoteCacheTTL: "1ms"}
	return NewManager(cfg, nil, provs)
}

func TestBestQuotePicksCheapest(t *testing.T) {
	mgr := newTestManager("cheapest", map[string]Provider{
		"expensive": &fakeProvider{name: "expensive", cost: 2.45, billed: 64400},
		"cheap":     &fakeProvider{name: "cheap", cost: 1.69, billed: 32000},
		"trx_burn":  &fakeProvider{name: "trx_burn", cost: 3.20, billed: 32000},
	})
	q, err := mgr.BestQuote(context.Background(), QuoteRequest{Resource: ResourceEnergy, Amount: 32000})
	if err != nil {
		t.Fatalf("BestQuote: %v", err)
	}
	if q.Provider != "cheap" {
		t.Fatalf("provider = %s, want cheap", q.Provider)
	}
}

// A provider that is out of stock or unreachable must not block the sweep: it
// simply drops out of the comparison.
func TestBestQuoteSkipsFailingProviders(t *testing.T) {
	mgr := newTestManager("cheapest", map[string]Provider{
		"broken":   &fakeProvider{name: "broken", err: errors.New("out of stock")},
		"trx_burn": &fakeProvider{name: "trx_burn", cost: 3.20, billed: 32000},
	})
	q, err := mgr.BestQuote(context.Background(), QuoteRequest{Resource: ResourceEnergy, Amount: 32000})
	if err != nil {
		t.Fatalf("BestQuote: %v", err)
	}
	if q.Provider != "trx_burn" {
		t.Fatalf("provider = %s, want the trx_burn fallback", q.Provider)
	}
}

func TestBestQuoteFailsWhenEveryProviderFails(t *testing.T) {
	mgr := newTestManager("cheapest", map[string]Provider{
		"a": &fakeProvider{name: "a", err: errors.New("boom")},
		"b": &fakeProvider{name: "b", err: errors.New("boom")},
	})
	if _, err := mgr.BestQuote(context.Background(), QuoteRequest{Resource: ResourceEnergy, Amount: 32000}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
}

// Priority mode must prefer real rentals over burning TRX even when burning
// happens to quote cheaper at that moment.
func TestPriorityModeKeepsBurnLast(t *testing.T) {
	mgr := newTestManager("priority", map[string]Provider{
		"trx_burn":       &fakeProvider{name: "trx_burn", cost: 0.01, billed: 32000},
		"tronenergyrent": &fakeProvider{name: "tronenergyrent", cost: 1.69, billed: 32000},
	})
	q, err := mgr.BestQuote(context.Background(), QuoteRequest{Resource: ResourceEnergy, Amount: 32000})
	if err != nil {
		t.Fatal(err)
	}
	if q.Provider != "tronenergyrent" {
		t.Fatalf("provider = %s, want tronenergyrent", q.Provider)
	}
}

func TestFixedModeUsesConfiguredProvider(t *testing.T) {
	cfg := config.EnergyConfig{Mode: "fixed", Fixed: "trx_burn", DefaultPeriod: "1h"}
	mgr := NewManager(cfg, nil, map[string]Provider{
		"trx_burn":       &fakeProvider{name: "trx_burn", cost: 3.2, billed: 32000},
		"tronenergyrent": &fakeProvider{name: "tronenergyrent", cost: 1.0, billed: 32000},
	})
	q, err := mgr.BestQuote(context.Background(), QuoteRequest{Resource: ResourceEnergy, Amount: 32000})
	if err != nil {
		t.Fatal(err)
	}
	if q.Provider != "trx_burn" {
		t.Fatalf("provider = %s, want the pinned trx_burn", q.Provider)
	}
}

func TestFixedModeFailsWhenProviderMissing(t *testing.T) {
	cfg := config.EnergyConfig{Mode: "fixed", Fixed: "gasstation"}
	mgr := NewManager(cfg, nil, map[string]Provider{
		"trx_burn": &fakeProvider{name: "trx_burn"},
	})
	if _, err := mgr.BestQuote(context.Background(), QuoteRequest{Amount: 1}); err == nil {
		t.Fatal("a missing pinned provider must be an error, not a silent fallback")
	}
}

func TestQuoteCacheAvoidsRepeatedCalls(t *testing.T) {
	p := &fakeProvider{name: "trx_burn", cost: 3.2, billed: 32000}
	cfg := config.EnergyConfig{Mode: "cheapest", DefaultPeriod: "1h", QuoteCacheTTL: "1m"}
	mgr := NewManager(cfg, nil, map[string]Provider{"trx_burn": p})
	for i := 0; i < 3; i++ {
		if _, err := mgr.BestQuote(context.Background(), QuoteRequest{Resource: ResourceEnergy, Amount: 32000}); err != nil {
			t.Fatal(err)
		}
	}
	if p.quotes != 1 {
		t.Fatalf("provider was quoted %d times, want 1 (cached)", p.quotes)
	}
}

func TestFeeMode(t *testing.T) {
	if got := FeeMode("trx_burn"); got != FeeModeBurn {
		t.Fatalf("got %s, want %s", got, FeeModeBurn)
	}
	if got := FeeMode("gasstation"); got != "rent:gasstation" {
		t.Fatalf("got %s", got)
	}
}

// A withdrawal must never pay the fee in TRX, so excluding the burn fallback
// has to surface as an error instead of a burn quote.
func TestBestQuoteExcludeBurnFailsWhenOnlyBurnIsLeft(t *testing.T) {
	mgr := newTestManager("cheapest", map[string]Provider{
		"broken":   &fakeProvider{name: "broken", err: errors.New("out of stock")},
		"trx_burn": &fakeProvider{name: "trx_burn", cost: 3.20, billed: 32000},
	})
	_, err := mgr.BestQuote(context.Background(), QuoteRequest{
		Resource: ResourceEnergy, Amount: 32000, ExcludeBurn: true})
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err = %v, want ErrNoProvider", err)
	}
}

func TestBestQuoteExcludeBurnPicksRental(t *testing.T) {
	mgr := newTestManager("cheapest", map[string]Provider{
		"trx_burn":       &fakeProvider{name: "trx_burn", cost: 0.01, billed: 32000},
		"tronenergyrent": &fakeProvider{name: "tronenergyrent", cost: 1.69, billed: 32000},
	})
	q, err := mgr.BestQuote(context.Background(), QuoteRequest{
		Resource: ResourceEnergy, Amount: 32000, ExcludeBurn: true})
	if err != nil {
		t.Fatal(err)
	}
	if q.Provider != "tronenergyrent" {
		t.Fatalf("provider = %s, want tronenergyrent", q.Provider)
	}
}

func TestFixedBurnModeRejectsExcludeBurn(t *testing.T) {
	cfg := config.EnergyConfig{Mode: "fixed", Fixed: ProviderTRXBurn, DefaultPeriod: "1h"}
	mgr := NewManager(cfg, nil, map[string]Provider{
		"trx_burn": &fakeProvider{name: "trx_burn", cost: 3.2, billed: 32000},
	})
	_, err := mgr.BestQuote(context.Background(), QuoteRequest{
		Resource: ResourceEnergy, Amount: 32000, ExcludeBurn: true})
	if !errors.Is(err, ErrBurnNotAllowed) {
		t.Fatalf("err = %v, want ErrBurnNotAllowed", err)
	}
}

// With rental switched off in the config the burn provider is the only payer
// left, so a caller that normally refuses to burn (withdraw, the hot wallet
// pool) must get the burn quote instead of ErrBurnNotAllowed.
func TestRentalDisabledIgnoresExcludeBurn(t *testing.T) {
	off := false
	cfg := config.EnergyConfig{Mode: "fixed", Fixed: ProviderTRXBurn,
		DefaultPeriod: "1h", RentalEnabled: &off}
	mgr := NewManager(cfg, nil, map[string]Provider{
		"trx_burn": &fakeProvider{name: "trx_burn", cost: 3.2, billed: 32000},
	})
	if mgr.RentalEnabled() {
		t.Fatal("RentalEnabled = true, want false")
	}
	q, err := mgr.BestQuote(context.Background(), QuoteRequest{
		Resource: ResourceEnergy, Amount: 32000, ExcludeBurn: true})
	if err != nil {
		t.Fatal(err)
	}
	if q.Provider != ProviderTRXBurn {
		t.Fatalf("provider = %s, want %s", q.Provider, ProviderTRXBurn)
	}
}

func TestBurnCostSunBillsOnlyTheMissingResources(t *testing.T) {
	params := &chain.ChainParameters{EnergyFeeSun: 210, TransactionFeeSun: 1000}
	// Nothing on the account: the whole estimate plus the full transfer size.
	if got, want := BurnCostSun(&chain.AccountResource{}, params, 65000),
		int64(65000*210+TransferBytes*1000); got != want {
		t.Fatalf("empty account cost = %d, want %d", got, want)
	}
	// A delegation and the free bandwidth quota cover everything.
	res := &chain.AccountResource{EnergyLimit: 100000, FreeNetLimit: 600}
	if got := BurnCostSun(res, params, 65000); got != 0 {
		t.Fatalf("covered account cost = %d, want 0", got)
	}
	// Partial cover is billed for the remainder only.
	res = &chain.AccountResource{EnergyLimit: 60000, EnergyUsed: 5000, FreeNetLimit: 300}
	if got, want := BurnCostSun(res, params, 65000), int64(10000*210+45*1000); got != want {
		t.Fatalf("partial cover cost = %d, want %d", got, want)
	}
}
