package energy

import (
	"testing"

	"github.com/hongkongstar6/trc20/internal/config"
)

func TestMinSweepUSDTFixedOverridesRuntimeThreshold(t *testing.T) {
	p := NewPricer(config.SweepThresholdConfig{FixedUSDT: 100, MinUSDT: 50}, config.EnergyConfig{}, nil, nil)
	p.threshold = 320
	if got := p.MinSweepUSDT(); got != 100 {
		t.Fatalf("MinSweepUSDT = %v, want 100", got)
	}
}

func TestMinSweepUSDTWithoutFixedFallsBackToMin(t *testing.T) {
	p := NewPricer(config.SweepThresholdConfig{MinUSDT: 50}, config.EnergyConfig{}, nil, nil)
	if got := p.MinSweepUSDT(); got != 50 {
		t.Fatalf("MinSweepUSDT = %v, want 50", got)
	}
	p.threshold = 320
	if got := p.MinSweepUSDT(); got != 320 {
		t.Fatalf("MinSweepUSDT = %v, want 320", got)
	}
}
