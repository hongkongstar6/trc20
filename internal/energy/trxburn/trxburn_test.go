package trxburn

import (
	"context"
	"testing"

	"github.com/hongkongstar6/trc20/internal/energy"
)

// The burn fallback must price energy from the live on-chain rate, otherwise
// comparing it against rental quotes is meaningless.
func TestQuoteUsesChainEnergyFee(t *testing.T) {
	p := New(100)
	q, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 32000})
	if err != nil {
		t.Fatal(err)
	}
	// 32000 energy * 100 sun = 3,200,000 sun = 3.2 TRX
	if q.CostTRX < 3.19 || q.CostTRX > 3.21 {
		t.Fatalf("cost = %f TRX, want 3.2", q.CostTRX)
	}
	if q.BilledUnits != 32000 {
		t.Fatalf("billed = %d, want 32000", q.BilledUnits)
	}
}

func TestSetEnergyFeeChangesQuote(t *testing.T) {
	p := New(100)
	p.SetEnergyFee(420)
	q, err := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if q.CostTRX < 4.19 || q.CostTRX > 4.21 {
		t.Fatalf("cost = %f TRX, want 4.2 after the fee proposal", q.CostTRX)
	}
	// A nonsensical value must be ignored rather than making energy free.
	p.SetEnergyFee(0)
	q2, _ := p.Quote(context.Background(), energy.QuoteRequest{Resource: energy.ResourceEnergy, Amount: 10000})
	if q2.CostTRX != q.CostTRX {
		t.Fatalf("cost changed to %f after an invalid fee update", q2.CostTRX)
	}
}

// Burning needs no order, so Ensure must resolve immediately: this is what
// keeps Nile (where no rental platform has a test environment) working.
func TestEnsureIsImmediatelyDelegated(t *testing.T) {
	p := New(100)
	order, err := p.Ensure(context.Background(), energy.OrderRequest{IdempotencyKey: "req-1", Amount: 32000})
	if err != nil {
		t.Fatal(err)
	}
	if order.State != energy.StateDelegated || order.RequestID != "req-1" {
		t.Fatalf("unexpected order %+v", order)
	}
}
