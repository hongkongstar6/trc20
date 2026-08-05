package energy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/store"
)

// FeeModeBurn is recorded on transactions that paid by burning TRX.
const FeeModeBurn = "burn"

// Manager selects a provider, places the rental and waits for delegation.
// Callers never talk to a provider directly.
type Manager struct {
	cfg   config.EnergyConfig
	st    *store.Store
	gw    *chain.Gateway
	log   *slog.Logger
	provs map[string]Provider

	mu     sync.Mutex
	quotes map[string]cachedQuote
}

type cachedQuote struct {
	quote   *Quote
	expires time.Time
}

func NewManager(cfg config.EnergyConfig, st *store.Store, gw *chain.Gateway, log *slog.Logger, provs map[string]Provider) *Manager {
	return &Manager{cfg: cfg, st: st, gw: gw, log: log, provs: provs, quotes: map[string]cachedQuote{}}
}

func (m *Manager) Providers() map[string]Provider { return m.provs }

// BestQuote ranks the enabled providers according to energy.mode. Providers
// that error out (out of stock, low prepaid balance, unreachable) are simply
// excluded, and trx_burn always remains as the last resort.
func (m *Manager) BestQuote(ctx context.Context, req QuoteRequest) (*Quote, error) {
	if req.Period == "" {
		req.Period = m.defaultPeriod()
	}
	if m.cfg.Mode == "fixed" {
		p, ok := m.provs[m.cfg.Fixed]
		if !ok {
			return nil, fmt.Errorf("energy: fixed provider %q is not enabled", m.cfg.Fixed)
		}
		return m.quoteOne(ctx, p, req)
	}

	names := m.orderedNames()
	type result struct {
		quote *Quote
		err   error
	}
	results := make([]result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			q, err := m.quoteOne(ctx, p, req)
			results[i] = result{quote: q, err: err}
		}(i, m.provs[name])
	}
	wg.Wait()

	var best *Quote
	var errs []string
	for i, r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", names[i], r.err))
			continue
		}
		if m.cfg.Mode == "priority" {
			return r.quote, nil // names are already priority ordered
		}
		if best == nil || r.quote.CostTRX < best.CostTRX {
			best = r.quote
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoProvider, strings.Join(errs, "; "))
	}
	return best, nil
}

// orderedNames returns providers with the burn fallback last, so priority mode
// and the cheapest tie-break both prefer real rentals.
func (m *Manager) orderedNames() []string {
	names := make([]string, 0, len(m.provs))
	for n := range m.provs {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		bi, bj := names[i] == "trx_burn", names[j] == "trx_burn"
		if bi != bj {
			return bj
		}
		return names[i] < names[j]
	})
	return names
}

func (m *Manager) quoteOne(ctx context.Context, p Provider, req QuoteRequest) (*Quote, error) {
	key := fmt.Sprintf("%s|%s|%d|%s", p.Name(), req.Resource, req.Amount, req.Period)
	ttl := config.Duration(m.cfg.QuoteCacheTTL, 45*time.Second)
	m.mu.Lock()
	if c, ok := m.quotes[key]; ok && time.Now().Before(c.expires) {
		m.mu.Unlock()
		return c.quote, nil
	}
	m.mu.Unlock()

	q, err := p.Quote(ctx, req)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.quotes[key] = cachedQuote{quote: q, expires: time.Now().Add(ttl)}
	m.mu.Unlock()
	return q, nil
}

func (m *Manager) defaultPeriod() string {
	if m.cfg.DefaultPeriod != "" {
		return m.cfg.DefaultPeriod
	}
	return "1h"
}

// Acquire rents energy for one address and waits until it is delegated.
//
// Order of operations matters: the local row is written *before* the provider
// call, because tronenergyrent has no idempotency key and a timed out request
// may still have created a paid order. On timeout the row lets us reconcile
// instead of paying twice.
func (m *Manager) Acquire(ctx context.Context, purpose, receiver string, need int64, requestID string) (*model.EnergyRentOrder, error) {
	quote, err := m.BestQuote(ctx, QuoteRequest{Resource: ResourceEnergy, Amount: need, Period: m.defaultPeriod(), Receiver: receiver})
	if err != nil {
		return nil, err
	}
	provider := m.provs[quote.Provider]
	row := &model.EnergyRentOrder{
		Provider:        quote.Provider,
		RequestID:       requestID,
		ReceiveAddress:  receiver,
		ResourceType:    ResourceEnergy,
		RequestedEnergy: quote.BilledUnits,
		Period:          quote.Period,
		CostTRX:         quote.CostTRX,
		Status:          model.EnergyOrderCreated,
		Purpose:         purpose,
	}
	if err := m.st.DB.WithContext(ctx).Create(row).Error; err != nil {
		// A duplicate request id means this rental was already attempted.
		var existing model.EnergyRentOrder
		if e := m.st.DB.WithContext(ctx).Where("request_id = ?", requestID).Take(&existing).Error; e == nil {
			row = &existing
		} else {
			return nil, err
		}
	}
	if quote.Provider == "trx_burn" {
		row.Status = model.EnergyOrderDelegated
		m.markDelegated(ctx, row, &Order{State: StateDelegated, ProviderState: FeeModeBurn})
		return row, nil
	}

	order, err := provider.Ensure(ctx, OrderRequest{
		Resource:       ResourceEnergy,
		Amount:         quote.BilledUnits,
		Period:         quote.Period,
		Receiver:       receiver,
		IdempotencyKey: requestID,
	})
	if err != nil {
		m.st.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
			"status":          model.EnergyOrderFailed,
			"provider_status": truncate(err.Error(), 32),
			"updated_at":      time.Now(),
		})
		return nil, fmt.Errorf("energy: %s order failed: %w", quote.Provider, err)
	}
	m.st.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
		"provider_order_id": order.ProviderOrderID,
		"provider_status":   order.ProviderState,
		"updated_at":        time.Now(),
	})
	row.ProviderOrderID = order.ProviderOrderID

	final, err := m.wait(ctx, provider, row)
	if err != nil {
		return nil, err
	}
	m.markDelegated(ctx, row, final)
	return row, nil
}

// wait polls the provider (neither platform has a usable callback) and then
// verifies the delegation on chain, because the provider saying "delegated" is
// not proof that our address can actually spend the energy.
func (m *Manager) wait(ctx context.Context, provider Provider, row *model.EnergyRentOrder) (*Order, error) {
	timeout := config.Duration(m.cfg.WaitTimeout, 90*time.Second)
	deadline := time.Now().Add(timeout)
	pollKey := row.ProviderOrderID
	if provider.Name() == "gasstation" {
		pollKey = row.RequestID
	}
	var last *Order
	for time.Now().Before(deadline) {
		order, err := provider.Poll(ctx, pollKey)
		if err != nil {
			m.log.Warn("poll energy order failed", "provider", provider.Name(), "order", pollKey, "err", err)
		} else {
			last = order
			switch order.State {
			case StateDelegated:
				if ok, err := m.confirmOnChain(ctx, row.ReceiveAddress, row.RequestedEnergy); err == nil && ok {
					return order, nil
				}
			case StateFailed, StateCancelled:
				m.st.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
					"status": model.EnergyOrderFailed, "provider_status": order.ProviderState, "updated_at": time.Now(),
				})
				return nil, fmt.Errorf("energy: %s order %s ended in state %s", provider.Name(), pollKey, order.ProviderState)
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	state := "unknown"
	if last != nil {
		state = last.ProviderState
	}
	return nil, fmt.Errorf("energy: timed out waiting for %s order %s (last state %s)", provider.Name(), pollKey, state)
}

// confirmOnChain checks that the delegated energy is actually usable.
func (m *Manager) confirmOnChain(ctx context.Context, addr string, need int64) (bool, error) {
	res, err := m.gw.GetAccountResource(ctx, addr)
	if err != nil {
		return false, err
	}
	// Providers round the delegation, so accept a small shortfall.
	return res.AvailableEnergy() >= need*95/100, nil
}

func (m *Manager) markDelegated(ctx context.Context, row *model.EnergyRentOrder, order *Order) {
	now := time.Now()
	updates := map[string]any{
		"status":      model.EnergyOrderDelegated,
		"finished_at": now,
		"updated_at":  now,
	}
	if order != nil {
		updates["provider_status"] = order.ProviderState
		updates["delegate_txid"] = order.DelegateTxID
		if order.DelegatedEnergy > 0 {
			updates["delegated_energy"] = order.DelegatedEnergy
		}
		if order.CostTRX > 0 {
			updates["cost_trx"] = order.CostTRX
		}
	}
	if err := m.st.DB.WithContext(ctx).Model(&model.EnergyRentOrder{}).
		Where("id = ?", row.ID).UpdateColumns(updates).Error; err != nil {
		m.log.Error("update energy order failed", "id", row.ID, "err", err)
	}
	row.Status = model.EnergyOrderDelegated
}

// EstimateEnergy asks the chain how much energy a transfer would consume.
// A recipient that never held the token needs roughly twice as much (a new
// storage slot), and guessing a single value makes those transfers fail with
// OUT_OF_ENERGY while still paying the fee.
func (m *Manager) EstimateEnergy(ctx context.Context, owner, contract, data string) (int64, error) {
	_, used, err := m.gw.TriggerConstantContract(ctx, owner, contract, data)
	if err != nil {
		return 0, err
	}
	if used <= 0 {
		return m.cfg.EnergyPerTxNew, nil
	}
	// Head room for price/slot differences between estimation and execution.
	return used * 115 / 100, nil
}

// FeeMode renders the value stored on sweep/withdraw rows.
func FeeMode(provider string) string {
	if provider == "trx_burn" {
		return FeeModeBurn
	}
	return "rent:" + provider
}

// PendingOrders lists rental orders stuck in the created state, which the
// reconciliation job uses to detect paid-but-unconfirmed orders.
func PendingOrders(ctx context.Context, db *gorm.DB, olderThan time.Duration) ([]model.EnergyRentOrder, error) {
	var rows []model.EnergyRentOrder
	err := db.WithContext(ctx).
		Where("status = ? AND created_at < ?", model.EnergyOrderCreated, time.Now().Add(-olderThan)).
		Limit(200).Find(&rows).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return rows, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
