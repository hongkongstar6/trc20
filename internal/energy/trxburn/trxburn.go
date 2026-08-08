// Package trxburn is the always-available fallback provider: instead of
// renting energy it simply lets the transaction burn TRX. It is also the only
// provider usable on Nile, because neither rental platform has a test
// environment.
package trxburn

import (
	"context"
	"strconv"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
)

const Name = "trx_burn"

func init() {
	f := func(conf config.ProviderConf) (energy.Provider, error) {
		fee, err := strconv.ParseInt(energy.Option(conf, "energy_fee_sun", "100"), 10, 64)
		if err != nil {
			return nil, err
		}
		return &Provider{energyFeeSun: fee}, nil
	}
	energy.Register(Name, f)
}

// Provider reports the burn cost so the comparison engine can rank it against
// the rental platforms with the same units.
type Provider struct {
	energyFeeSun int64
}

// New builds the provider directly (used when no config block exists).
func New(energyFeeSun int64) *Provider {
	if energyFeeSun <= 0 {
		energyFeeSun = 100
	}
	return &Provider{energyFeeSun: energyFeeSun}
}

// SetEnergyFee updates the on-chain price so quotes stay accurate after a
// chain parameter proposal changes getEnergyFee.
func (p *Provider) SetEnergyFee(sun int64) {
	if sun > 0 {
		p.energyFeeSun = sun
	}
}

func (p *Provider) Name() string { return Name }

func (p *Provider) Quote(_ context.Context, req energy.QuoteRequest) (*energy.Quote, error) {
	return &energy.Quote{
		Provider:    Name,
		CostTRX:     float64(req.Amount) * float64(p.energyFeeSun) / 1e6,
		BilledUnits: req.Amount,
		Period:      req.Period,
	}, nil
}

// Ensure is a no-op: burning needs no order, the transaction pays on execution.
func (p *Provider) Ensure(_ context.Context, req energy.OrderRequest) (*energy.Order, error) {
	return &energy.Order{
		Provider:      Name,
		RequestID:     req.IdempotencyKey,
		State:         energy.StateDelegated,
		ProviderState: "burn",
	}, nil
}

func (p *Provider) Poll(_ context.Context, requestID string) (*energy.Order, error) {
	return &energy.Order{Provider: Name, RequestID: requestID, State: energy.StateDelegated, ProviderState: "burn"}, nil
}

// Balance is unlimited from the provider's point of view; the actual limit is
// the TRX balance of the signing address, which the workers check separately.
func (p *Provider) Balance(context.Context) (float64, string, error) {
	return 0, "", nil
}
