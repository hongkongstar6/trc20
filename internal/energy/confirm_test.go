package energy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
)

const testReceiver = "TZJ9qkoxUB1SGdbtChgAjUphBmkJwAeBaW"

// resourceGateway serves getaccountresource with a fixed available energy.
func resourceGateway(t *testing.T, energyLimit, energyUsed int64) (*chain.Gateway, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"EnergyLimit": energyLimit,
			"energy_used": energyUsed,
		})
	}))
	gw, err := chain.NewGateway(config.ChainConfig{
		ChainNodes:   []config.NodeConfig{{Name: "primary", Endpoint: srv.URL, Priority: 1, Enabled: true, Timeout: "5s"}},
		RetryPerNode: 1,
	})
	if err != nil {
		srv.Close()
		t.Fatalf("NewGateway: %v", err)
	}
	return gw, srv.Close
}

// The hot wallet already holds delegated energy from earlier orders. Comparing
// the absolute balance against the requested amount would report every new
// rental as delegated the moment it is placed, which is exactly the case where
// the transfer is then broadcast without the energy it paid for.
func TestConfirmOnChainIgnoresEnergyThatWasAlreadyThere(t *testing.T) {
	gw, closeFn := resourceGateway(t, 200_000, 0)
	defer closeFn()
	mgr := NewManager(config.EnergyConfig{Mode: "cheapest"}, gw, nil)

	ok, err := mgr.confirmOnChain(context.Background(), testReceiver, 200_000, 65_000)
	if err != nil {
		t.Fatalf("confirmOnChain: %v", err)
	}
	if ok {
		t.Fatal("a delegation that never arrived must not be confirmed by the pre-existing balance")
	}
}

func TestConfirmOnChainAcceptsTheDeltaAboveTheBaseline(t *testing.T) {
	gw, closeFn := resourceGateway(t, 265_000, 0)
	defer closeFn()
	mgr := NewManager(config.EnergyConfig{Mode: "cheapest"}, gw, nil)

	ok, err := mgr.confirmOnChain(context.Background(), testReceiver, 200_000, 65_000)
	if err != nil {
		t.Fatalf("confirmOnChain: %v", err)
	}
	if !ok {
		t.Fatal("energy delegated on top of the baseline must be confirmed")
	}
}

// Every OUT_OF_ENERGY retry must rent more than the attempt that just failed,
// otherwise the retry burns the fee for the same failure again.
func TestRetrySafetyFactorGrowsAndIsCapped(t *testing.T) {
	first := RetrySafetyFactor(0)
	if first != DefaultSafetyFactor {
		t.Fatalf("first attempt factor = %v, want %v", first, DefaultSafetyFactor)
	}
	second, third, fourth := RetrySafetyFactor(1), RetrySafetyFactor(2), RetrySafetyFactor(3)
	if !(second > first && third > second) {
		t.Fatalf("factors must grow: %v, %v, %v", first, second, third)
	}
	if fourth != third {
		t.Fatalf("factor must stop growing: %v then %v", third, fourth)
	}
}
