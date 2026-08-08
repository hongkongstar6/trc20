package energy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/sirupsen/logrus"
)

// Pricer computes the sweep threshold at runtime from the current provider
// quote, the current chain parameters and the current TRX price. Nothing is
// hard coded: an energy price proposal or a TRX rally changes the answer.
//
//	cost_trx  = cheapest energy quote + bandwidth burn when the free quota is short
//	cost_usd  = cost_trx * trx_usd
//	min_sweep = max(cost_usd / target_cost_ratio, cost_usd * safety_multiple)
//	min_sweep = clamp(min_sweep, min_usdt, max_usdt)
type Pricer struct {
	cfg       config.SweepThresholdConfig
	energy    config.EnergyConfig
	mgr       *Manager
	gw        *chain.Gateway
	log       *logrus.Logger
	mu        sync.RWMutex
	threshold float64
	costUSD   float64
	trxUSD    float64
	refreshed time.Time
}

func NewPricer(threshold config.SweepThresholdConfig, energyCfg config.EnergyConfig, mgr *Manager, gw *chain.Gateway, log *logrus.Logger) *Pricer {
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Pricer{cfg: threshold, energy: energyCfg, mgr: mgr, gw: gw, log: log, trxUSD: threshold.TRXPriceUSD}
}

// Run refreshes the threshold periodically and logs every change for audit.
func (p *Pricer) Run(ctx context.Context) error {
	interval := config.Duration(p.cfg.RefreshInterval, 10*time.Minute)
	if err := p.Refresh(ctx); err != nil {
		p.log.Error("initial threshold refresh failed", ",err:", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.Refresh(ctx); err != nil {
				p.log.Error("threshold refresh failed", ",err:", err)
			}
		}
	}
}

// MinSweepUSDT returns the current threshold, falling back to the configured
// minimum before the first successful refresh.
func (p *Pricer) MinSweepUSDT() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.threshold > 0 {
		return p.threshold
	}
	return p.cfg.MinUSDT
}

// SweepCostUSD is the current expected cost of one sweep, used for reporting.
func (p *Pricer) SweepCostUSD() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.costUSD
}

func (p *Pricer) Refresh(ctx context.Context) error {
	params, err := p.gw.GetChainParameters(ctx)
	if err != nil {
		return fmt.Errorf("chain parameters: %w", err)
	}
	// Keep the burn fallback quoting at the live on-chain energy price.
	for _, prov := range p.mgr.Providers() {
		if setter, ok := prov.(interface{ SetEnergyFee(int64) }); ok {
			setter.SetEnergyFee(params.EnergyFeeSun)
		}
	}
	need := p.energy.EnergyPerTx
	quote, err := p.mgr.BestQuote(ctx, QuoteRequest{Resource: ResourceEnergy, Amount: need, Period: p.mgr.defaultPeriod()})
	if err != nil {
		return fmt.Errorf("quote: %w", err)
	}
	// Bandwidth is burned rather than rented: a TRC20 transfer is about 345
	// bytes, which costs less than any provider minimum bandwidth order.
	const bandwidthBytes = 345
	bandwidthTRX := float64(bandwidthBytes) * float64(params.TransactionFeeSun) / 1e6
	costTRX := quote.CostTRX + bandwidthTRX

	trxUSD, err := p.trxPrice(ctx)
	if err != nil {
		p.log.Warn("trx price lookup failed, using previous value", ",err:", err)
	}
	if trxUSD <= 0 {
		return fmt.Errorf("no TRX price available")
	}
	costUSD := costTRX * trxUSD
	threshold := costUSD / p.cfg.TargetCostRatio
	if floor := costUSD * p.cfg.SafetyMultiple; floor > threshold {
		threshold = floor
	}
	if p.cfg.MinUSDT > 0 && threshold < p.cfg.MinUSDT {
		threshold = p.cfg.MinUSDT
	}
	if p.cfg.MaxUSDT > 0 && threshold > p.cfg.MaxUSDT {
		threshold = p.cfg.MaxUSDT
	}

	p.mu.Lock()
	previous := p.threshold
	p.threshold, p.costUSD, p.trxUSD, p.refreshed = threshold, costUSD, trxUSD, time.Now()
	p.mu.Unlock()

	if previous != threshold {
		p.log.Info("sweep threshold updated",
			"provider", quote.Provider,
			"energy", need,
			"energy_fee_sun", params.EnergyFeeSun,
			"cost_trx", costTRX,
			"trx_usd", trxUSD,
			"cost_usd", costUSD,
			"min_sweep_usdt", threshold,
			"previous", previous)
	}
	return nil
}

// trxPrice reads the TRX/USD rate. A configured static price wins so a market
// data outage cannot stall sweeping.
func (p *Pricer) trxPrice(ctx context.Context) (float64, error) {
	if p.cfg.TRXPriceUSD > 0 {
		return p.cfg.TRXPriceUSD, nil
	}
	p.mu.RLock()
	previous := p.trxUSD
	p.mu.RUnlock()
	if p.cfg.TRXPriceURL == "" {
		return previous, fmt.Errorf("no trx price source configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.TRXPriceURL, nil)
	if err != nil {
		return previous, err
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return previous, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return previous, fmt.Errorf("price http %d", resp.StatusCode)
	}
	// Accepts both {"price":"0.32"} and CoinGecko style {"tron":{"usd":0.32}}.
	var generic map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&generic); err != nil {
		return previous, err
	}
	if v, ok := extractPrice(generic); ok {
		return v, nil
	}
	return previous, fmt.Errorf("no price field found in response")
}

func extractPrice(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		if t > 0 {
			return t, true
		}
	case string:
		var f float64
		if _, err := fmt.Sscanf(t, "%g", &f); err == nil && f > 0 {
			return f, true
		}
	case map[string]any:
		for _, key := range []string{"price", "usd", "tron", "data"} {
			if inner, ok := t[key]; ok {
				if f, ok := extractPrice(inner); ok {
					return f, true
				}
			}
		}
	}
	return 0, false
}
