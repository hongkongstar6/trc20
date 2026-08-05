// Package energy abstracts TRC20 fee supply. Every rental platform is a
// Provider registered in a global registry, so adding a new platform means one
// implementation plus a config block and no changes to sweep/withdraw logic.
package energy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/hongkongstar6/trc20/internal/config"
)

// Resource types a provider can supply.
const (
	ResourceEnergy    = "energy"
	ResourceBandwidth = "bandwidth"
)

// Normalised order states across providers.
const (
	StatePending   = "pending"
	StateDelegated = "delegated"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

var ErrNoProvider = errors.New("energy: no provider available")

// QuoteRequest asks a provider what one rental would cost right now.
type QuoteRequest struct {
	Resource string
	Amount   int64  // energy units or bandwidth bytes
	Period   string // 1h | 1d | 3d | 30d (providers map this to their own codes)
	Receiver string
}

// Quote is a provider answer. CostTRX is the provider reported total, not a
// unit price times amount: providers add surcharges below their minimums.
type Quote struct {
	Provider    string
	CostTRX     float64
	BilledUnits int64 // may exceed the request because of provider minimums
	Available   int64 // remaining stock, 0 means unknown
	Period      string
}

// OrderRequest places a rental. IdempotencyKey is stored locally first so a
// timeout can be reconciled instead of retried blindly.
type OrderRequest struct {
	Resource       string
	Amount         int64
	Period         string
	Receiver       string
	IdempotencyKey string
	// Estimate hints (only some providers use them).
	Contract  string
	ToAddress string
}

// Order is the normalised order view.
type Order struct {
	Provider        string
	ProviderOrderID string
	RequestID       string
	State           string
	ProviderState   string
	DelegatedEnergy int64
	CostTRX         float64
	DelegateTxID    string
	Message         string
}

// Provider is the contract every rental platform implements.
type Provider interface {
	Name() string
	// Quote returns the current total price, or an error when the provider
	// cannot serve the request (out of stock, below minimum, unreachable).
	Quote(ctx context.Context, req QuoteRequest) (*Quote, error)
	// Ensure places an order (or returns the existing one for the same key).
	Ensure(ctx context.Context, req OrderRequest) (*Order, error)
	// Poll returns the current state of a previously placed order.
	Poll(ctx context.Context, requestID string) (*Order, error)
	// Balance returns the prepaid TRX balance and the platform deposit
	// address, which the auto topup guard compares against its whitelist.
	Balance(ctx context.Context) (balanceTRX float64, depositAddress string, err error)
}

// Factory builds a provider from its config block.
type Factory func(conf config.ProviderConf) (Provider, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register is called from provider package init functions.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

// Build instantiates every enabled provider from the config.
func Build(cfg config.EnergyConfig) (map[string]Provider, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := map[string]Provider{}
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		conf := cfg.Providers[name]
		if !conf.Enabled {
			continue
		}
		f, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("energy: unknown provider %q (registered: %v)", name, registeredNames())
		}
		p, err := f(conf)
		if err != nil {
			return nil, fmt.Errorf("energy: build provider %s: %w", name, err)
		}
		out[name] = p
	}
	if len(out) == 0 {
		return nil, ErrNoProvider
	}
	return out, nil
}

func registeredNames() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Option reads a provider option with a default.
func Option(conf config.ProviderConf, key, def string) string {
	if v, ok := conf.Options[key]; ok && v != "" {
		return v
	}
	return def
}
