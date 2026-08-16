package energy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/sirupsen/logrus"
)

// FeeModeBurn is recorded on transactions that paid by burning TRX.
const FeeModeBurn = "burn"

// ProviderTRXBurn is the pseudo provider that pays the fee by burning the
// signer's TRX instead of renting energy.
const ProviderTRXBurn = config.ProviderTRXBurn

// ErrBurnNotAllowed is returned when the only usable provider would burn TRX
// but the caller asked for a real rental. Withdrawals use this so a rental
// outage stops the flow instead of silently paying the fee in TRX.
var ErrBurnNotAllowed = errors.New("energy: rental unavailable and burning TRX is not allowed")

// Manager selects a provider, places the rental and waits for delegation.
// Callers never talk to a provider directly.
type Manager struct {
	cfg config.EnergyConfig
	//st  *store.Store
	gw *chain.Gateway
	//log   *logrus.Logger
	provs map[string]Provider

	mu     sync.Mutex
	quotes map[string]cachedQuote
}

type cachedQuote struct {
	quote   *Quote
	expires time.Time
}

func NewManager(cfg config.EnergyConfig, gw *chain.Gateway, provs map[string]Provider) *Manager {
	return &Manager{cfg: cfg,
		//st: st,
		gw: gw,
		//log: log,
		provs: provs, quotes: map[string]cachedQuote{}}
}

func (m *Manager) Providers() map[string]Provider { return m.provs }

// RentalEnabled reports whether third party rental is configured. With rental
// off every caller pays its fee in TRX, so the burn fallback stops being a
// fallback and the callers must not treat it as an outage.
func (m *Manager) RentalEnabled() bool { return m.cfg.RentalOn() }

// BestQuote ranks the enabled providers according to energy.mode. Providers
// that error out (out of stock, low prepaid balance, unreachable) are simply
// excluded, and trx_burn always remains as the last resort.
func (m *Manager) BestQuote(ctx context.Context, req QuoteRequest) (*Quote, error) {
	if req.Period == "" {
		req.Period = m.defaultPeriod()
	}
	if !m.cfg.RentalOn() {
		req.ExcludeBurn = false
	}
	if m.cfg.Mode == "fixed" {
		if req.ExcludeBurn && m.cfg.Fixed == ProviderTRXBurn {
			return nil, ErrBurnNotAllowed
		}
		p, ok := m.provs[m.cfg.Fixed]
		if !ok {
			return nil, fmt.Errorf("energy: fixed provider %q is not enabled", m.cfg.Fixed)
		}
		return m.quoteOne(ctx, p, req)
	}

	names := m.orderedNames()
	if req.ExcludeBurn {
		kept := make([]string, 0, len(names))
		for _, n := range names {
			if n != ProviderTRXBurn {
				kept = append(kept, n)
			}
		}
		names = kept
		if len(names) == 0 {
			return nil, ErrBurnNotAllowed
		}
	}
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
		bi, bj := names[i] == ProviderTRXBurn, names[j] == ProviderTRXBurn
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
	return m.acquire(ctx, purpose, receiver, need, requestID, false)
}

// AcquireRented is Acquire without the trx_burn fallback: when no platform can
// deliver the energy it fails with ErrBurnNotAllowed instead of letting the
// transaction pay the fee out of the signing address' TRX.
func (m *Manager) AcquireRented(ctx context.Context, purpose, receiver string, need int64, requestID string) (*model.EnergyRentOrder, error) {
	return m.acquire(ctx, purpose, receiver, need, requestID, true)
}

func (m *Manager) acquire(ctx context.Context, purpose, receiver string, need int64, requestID string, excludeBurn bool) (*model.EnergyRentOrder, error) {
	if !m.cfg.RentalOn() {
		excludeBurn = false
	}
	quote, err := m.BestQuote(ctx, QuoteRequest{Resource: ResourceEnergy, Amount: need,
		Period: m.defaultPeriod(), Receiver: receiver, ExcludeBurn: excludeBurn})
	if err != nil {
		return nil, err
	}
	// The energy the address can already spend is the baseline the delegation is
	// measured against: comparing the absolute amount would accept an address
	// that was simply topped up before (the hot wallet always is) as delegated
	// while nothing arrived.
	baseline := m.availableEnergy(ctx, receiver)
	provider := m.provs[quote.Provider]
	row := &model.EnergyRentOrder{
		Provider:        quote.Provider,
		RequestID:       requestID,
		ReceiveAddress:  receiver,
		ResourceType:    ResourceEnergy,
		RequestedEnergy: quote.BilledUnits,
		Period:          quote.Period,
		CostTRX:         quote.CostTRX,
		Amount:          model.FormatTRX(quote.CostTRX),
		Status:          model.EnergyOrderCreated,
		Purpose:         purpose,
		BaselineEnergy:  baseline,
	}
	if err := store.MyStore.DB.WithContext(ctx).Create(row).Error; err != nil {
		// A duplicate request id means this rental was already attempted.
		var existing model.EnergyRentOrder
		if e := store.MyStore.DB.WithContext(ctx).Where("request_id = ?", requestID).Take(&existing).Error; e == nil {
			row = &existing
		} else {
			return nil, err
		}
	}
	if quote.Provider == ProviderTRXBurn {
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
		store.MyStore.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
			"status":          model.EnergyOrderFailed,
			"provider_status": truncate(err.Error(), 32),
			"updated_at":      time.Now(),
		})
		return nil, fmt.Errorf("energy: %s order failed: %w", quote.Provider, err)
	}
	store.MyStore.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
		"provider_order_id": order.ProviderOrderID,
		"provider_status":   order.ProviderState,
		"updated_at":        time.Now(),
	})
	row.ProviderOrderID = order.ProviderOrderID

	final, err := m.wait(ctx, provider, row, baseline)
	if err != nil {
		return nil, err
	}
	m.markDelegated(ctx, row, final)
	return row, nil
}

// AcquireBurn records the fee of a transfer that pays its own energy and
// bandwidth out of the signing address' TRX. No platform is contacted: the
// caller has already verified the address holds the TRX, and the row exists so
// the burn shows up in the fee accounting next to the rentals.
func (m *Manager) AcquireBurn(ctx context.Context, purpose, receiver string, need int64, requestID string) (*model.EnergyRentOrder, error) {
	cost := m.burnCostTRX(ctx, need)
	row := &model.EnergyRentOrder{
		Provider:        ProviderTRXBurn,
		RequestID:       requestID,
		ReceiveAddress:  receiver,
		ResourceType:    ResourceEnergy,
		RequestedEnergy: need,
		Period:          m.defaultPeriod(),
		CostTRX:         cost,
		Amount:          model.FormatTRX(cost),
		Status:          model.EnergyOrderCreated,
		Purpose:         purpose,
		BaselineEnergy:  m.availableEnergy(ctx, receiver),
	}
	if err := store.MyStore.DB.WithContext(ctx).Create(row).Error; err != nil {
		var existing model.EnergyRentOrder
		if e := store.MyStore.DB.WithContext(ctx).Where("request_id = ?", requestID).Take(&existing).Error; e != nil {
			return nil, err
		}
		row = &existing
	}
	m.markDelegated(ctx, row, &Order{State: StateDelegated, ProviderState: FeeModeBurn, CostTRX: cost})
	return row, nil
}

// burnCostTRX prices the burn through the trx_burn provider when it is built.
// An unpriced burn is still a valid burn, so a missing provider costs nothing
// but the accounting figure.
func (m *Manager) burnCostTRX(ctx context.Context, need int64) float64 {
	p, ok := m.provs[ProviderTRXBurn]
	if !ok {
		return 0
	}
	q, err := p.Quote(ctx, QuoteRequest{Resource: ResourceEnergy, Amount: need, Period: m.defaultPeriod()})
	if err != nil {
		return 0
	}
	return q.CostTRX
}

// wait polls the provider (neither platform has a usable callback) and then
// verifies the delegation on chain, because the provider saying "delegated" is
// not proof that our address can actually spend the energy.
func (m *Manager) wait(ctx context.Context, provider Provider, row *model.EnergyRentOrder, baseline int64) (*Order, error) {
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
			logrus.Warn("poll energy order failed", ",provider:", provider.Name(), ",order:", pollKey, ",err:", err)
		} else {
			last = order
			switch order.State {
			case StateDelegated:
				if ok, err := m.confirmOnChain(ctx, row.ReceiveAddress, baseline, row.RequestedEnergy); err == nil && ok {
					return order, nil
				}
			case StateFailed, StateCancelled:
				store.MyStore.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
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

// confirmOnChain checks that the rented energy actually arrived, by comparing
// against the amount the address could already spend before the order was
// placed. Anything else would accept pre-existing energy as the delegation.
func (m *Manager) confirmOnChain(ctx context.Context, addr string, baseline, need int64) (bool, error) {
	res, err := m.gw.GetAccountResource(ctx, addr)
	if err != nil {
		return false, err
	}
	// Providers round the delegation, so accept a small shortfall.
	return res.AvailableEnergy()-baseline >= need*95/100, nil
}

// availableEnergy reads the spendable energy of an address. A failed read
// returns 0, which only makes the delegation check stricter.
func (m *Manager) availableEnergy(ctx context.Context, addr string) int64 {
	res, err := m.gw.GetAccountResource(ctx, addr)
	if err != nil {
		logrus.Warn("energy baseline read failed, assuming zero", ",address:", addr, ",err:", err)
		return 0
	}
	available := res.AvailableEnergy()
	if available < 0 {
		return 0
	}
	return available
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
			updates["amount"] = model.FormatTRX(order.CostTRX)
		}
		if block := m.delegateBlock(ctx, order.DelegateTxID); block > 0 {
			updates["block_number"] = block
		}
	}
	if err := store.MyStore.DB.WithContext(ctx).Model(&model.EnergyRentOrder{}).
		Where("id = ?", row.ID).UpdateColumns(updates).Error; err != nil {
		logrus.Error("update energy order failed", ",id:", row.ID, ",err:", err)
	}
	row.Status = model.EnergyOrderDelegated
}

// delegateBlock reads the block the provider's delegation transaction landed
// in. It is audit data only, so an unreadable receipt is not an error: the
// order is still delegated and the column simply stays 0.
func (m *Manager) delegateBlock(ctx context.Context, txid string) int64 {
	if txid == "" || m.gw == nil {
		return 0
	}
	info, err := m.gw.GetTxInfoByID(ctx, txid)
	if err != nil {
		logrus.Warn("delegation block read failed", ",txid:", txid, ",err:", err)
		return 0
	}
	if info == nil {
		return 0
	}
	return info.BlockNumber
}

// EstimateEnergy asks the chain how much energy a transfer would consume.
// A recipient that never held the token needs roughly twice as much (a new
// storage slot), and guessing a single value makes those transfers fail with
// OUT_OF_ENERGY while still paying the fee.
func (m *Manager) EstimateEnergy(ctx context.Context, owner, contract, data string) (int64, error) {
	return m.EstimateEnergyFactor(ctx, owner, contract, data, DefaultSafetyFactor)
}

// DefaultSafetyFactor is the head room added to the estimate for price and slot
// differences between the estimation and the execution. A retry after
// OUT_OF_ENERGY raises it, see RetrySafetyFactor.
const DefaultSafetyFactor = 1.15

// RetrySafetyFactor escalates the head room per consecutive OUT_OF_ENERGY
// failure of the same address: 1.15, 1.3, 1.5, then capped.
func RetrySafetyFactor(attempt int) float64 {
	switch {
	case attempt <= 0:
		return DefaultSafetyFactor
	case attempt == 1:
		return 1.3
	default:
		return 1.5
	}
}

// EstimateEnergyFactor is EstimateEnergy with an explicit safety factor.
func (m *Manager) EstimateEnergyFactor(ctx context.Context, owner, contract, data string, factor float64) (int64, error) {
	_, used, err := m.gw.TriggerConstantContract(ctx, owner, contract, data)
	if err != nil {
		return 0, err
	}
	if factor < 1 {
		factor = DefaultSafetyFactor
	}
	if used <= 0 {
		return int64(float64(m.cfg.EnergyPerTxNew) * factor), nil
	}
	return int64(float64(used) * factor), nil
}

// TransferBytes is the size of a TRC20 transfer used for the bandwidth part of
// the burn estimate. Bandwidth is never rented: 345 bytes cost less than any
// provider's minimum bandwidth order.
const TransferBytes = 345

// BurnCostSun estimates what a transfer paying its own fee costs the signing
// address, given the resources it already holds. Only the missing energy and
// bandwidth are billed, so an address with a delegation or free quota left is
// not asked for TRX it does not need.
func BurnCostSun(res *chain.AccountResource, params *chain.ChainParameters, needEnergy int64) int64 {
	var haveEnergy, haveBandwidth int64
	if res != nil {
		haveEnergy, haveBandwidth = res.AvailableEnergy(), res.AvailableBandwidth()
	}
	missingEnergy := needEnergy - haveEnergy
	if missingEnergy < 0 {
		missingEnergy = 0
	}
	missingBandwidth := int64(TransferBytes) - haveBandwidth
	if missingBandwidth < 0 {
		missingBandwidth = 0
	}
	return missingEnergy*params.EnergyFeeSun + missingBandwidth*params.TransactionFeeSun
}

// FeeMode renders the value stored on sweep/withdraw rows.
func FeeMode(provider string) string {
	if provider == ProviderTRXBurn {
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

// RunReconcile settles rental orders left in the created state. Acquire writes
// the row before paying the provider, so a request that timed out (or a process
// that died mid rental) leaves a row for an order that may well have been paid.
// Without this loop that money is never accounted for.
func (m *Manager) RunReconcile(ctx context.Context) error {
	interval := config.Duration(m.cfg.ReconcileInterval, time.Minute)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := m.ReconcilePending(ctx); err != nil {
			logrus.Error("energy order reconcile failed", ",err:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ReconcilePending polls every rental order still in the created state and
// gives it a terminal status. An order that cannot be resolved before
// energy.pending_timeout is failed and logged as a possible paid-for-nothing
// rental, which is the only way an operator learns about it.
func (m *Manager) ReconcilePending(ctx context.Context) error {
	grace := config.Duration(m.cfg.PendingGrace, 2*time.Minute)
	rows, err := PendingOrders(ctx, store.MyStore.DB, grace)
	if err != nil {
		return err
	}
	timeout := config.Duration(m.cfg.PendingTimeout, 30*time.Minute)
	for i := range rows {
		row := rows[i]
		if err := m.reconcileOne(ctx, &row, timeout); err != nil {
			logrus.Error("energy order reconcile failed", ",request_id:", row.RequestID, ",err:", err)
		}
	}
	return nil
}

func (m *Manager) reconcileOne(ctx context.Context, row *model.EnergyRentOrder, timeout time.Duration) error {
	expired := time.Since(row.CreatedAt) > timeout
	provider, ok := m.provs[row.Provider]
	if !ok {
		// The provider was disabled while the order was in flight; there is no
		// API left to ask, so only the operator can settle it.
		return m.abandon(ctx, row, "provider disabled", expired)
	}
	pollKey := row.ProviderOrderID
	if provider.Name() == "gasstation" {
		pollKey = row.RequestID
	}
	if pollKey == "" {
		// Ensure never returned an order id: the provider may still have taken
		// the money, and nothing identifies the order to ask about.
		return m.abandon(ctx, row, "no provider order id", expired)
	}
	order, err := provider.Poll(ctx, pollKey)
	if err != nil {
		return m.abandon(ctx, row, "poll failed: "+err.Error(), expired)
	}
	switch order.State {
	case StateDelegated:
		// Paid and delivered, only our wait gave up too early. The row is
		// settled so the cost lands in the rental accounting; the energy itself
		// may already have expired, which is what the cost report shows.
		m.markDelegated(ctx, row, order)
		logrus.Warn("energy order was delegated after the wait timed out",
			",request_id:", row.RequestID, ",provider:", row.Provider, ",cost_trx:", row.CostTRX)
		return nil
	case StateFailed, StateCancelled:
		return store.MyStore.DB.WithContext(ctx).Model(&model.EnergyRentOrder{}).
			Where("id = ? AND status = ?", row.ID, model.EnergyOrderCreated).
			UpdateColumns(map[string]any{
				"status": model.EnergyOrderFailed, "provider_status": order.ProviderState,
				"finished_at": time.Now(), "updated_at": time.Now(),
			}).Error
	}
	return m.abandon(ctx, row, "still "+order.ProviderState, expired)
}

// abandon keeps an unresolved order pending until energy.pending_timeout and
// then fails it with a loud log line: the rental may have been paid for, so it
// needs a human to reconcile it against the provider statement.
func (m *Manager) abandon(ctx context.Context, row *model.EnergyRentOrder, reason string, expired bool) error {
	if !expired {
		logrus.Warn("energy order still unresolved", ",request_id:", row.RequestID,
			",provider:", row.Provider, ",reason:", reason)
		return nil
	}
	logrus.Error("energy order abandoned, verify it against the provider statement",
		",request_id:", row.RequestID, ",provider:", row.Provider,
		",provider_order_id:", row.ProviderOrderID, ",cost_trx:", row.CostTRX, ",reason:", reason)
	return store.MyStore.DB.WithContext(ctx).Model(&model.EnergyRentOrder{}).
		Where("id = ? AND status = ?", row.ID, model.EnergyOrderCreated).
		UpdateColumns(map[string]any{
			"status":          model.EnergyOrderAbandoned,
			"provider_status": truncate(reason, 32),
			"finished_at":     time.Now(),
			"updated_at":      time.Now(),
		}).Error
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
