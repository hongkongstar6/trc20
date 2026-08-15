// Package withdraw executes withdrawal orders submitted by the business
// system. The business system already debited or froze the user balance, so the
// only guarantee this package must provide is: one order_no results in at
// most one on-chain transfer, and its final outcome is always reported.
package withdraw

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/hongkongstar6/trc20/internal/tron"
	"github.com/sirupsen/logrus"
)

// FailCodeRejected marks an order the wallet refused before signing anything;
// it never touched the chain.
const FailCodeRejected = "risk_rejected"

// Halt codes are written on an order that keeps its created state: the wallet
// stopped before signing and an operator has to act (refill the hot wallet,
// restore the energy rental) before the order can move on.
const (
	FailCodeInsufficientBalance = "hot_wallet_insufficient"
	FailCodeEnergyRental        = "energy_rental_failed"
	// FailCodeInsufficientTRX is only used with energy.rental_enabled=false,
	// where the transfer burns the hot wallet's own TRX for energy and bandwidth.
	FailCodeInsufficientTRX = "hot_wallet_trx_insufficient"
)

// DefaultHaltMaxRetries is used when withdraw_server.halt_max_retries is unset:
// an order halted this many times is failed instead of retried forever.
const DefaultHaltMaxRetries = 10

// ErrHalted stops the current round for an order without failing it: the order
// stays created and is picked up again once the alert was acted on.
var ErrHalted = errors.New("withdraw: halted, manual handling required")

// ErrHaltedFailed is returned once a halted order exhausted its retries and was
// failed back to the business system.
var ErrHaltedFailed = errors.New("withdraw: halted too many times, order failed")

type Worker struct {
	//cfg  *config.Config
	//st   *store.Store
	gw   *chain.Gateway
	sign *signer.Client
	mgr  *energy.Manager
	pool *energy.Pool
	//log   *logrus.Logger
	// tokens are every enabled token, keyed by upper case symbol: an order pays
	// out the contract of its own symbol, never the first configured one.
	tokens map[string]config.TokenConfig
}

func New(st *store.Store, gw *chain.Gateway, sign *signer.Client, mgr *energy.Manager, pool *energy.Pool, log *logrus.Logger) (*Worker, error) {
	tokens := map[string]config.TokenConfig{}
	for _, t := range config.Cfg.EnabledTokens() {
		tokens[strings.ToUpper(t.Symbol)] = t
	}
	if len(tokens) == 0 {
		return nil, errors.New("withdraw: no enabled token configured")
	}
	if config.Cfg.Wallet.HotWallet.Address == "" || config.Cfg.Wallet.HotWallet.Path == "" {
		return nil, errors.New("withdraw: wallet.hot_wallet address and path are required")
	}
	return &Worker{gw: gw, sign: sign, mgr: mgr, pool: pool, //log: log,
		tokens: tokens}, nil
}

// token resolves the token of an order. A symbol that is no longer configured
// has no contract to transfer, so the order must be rejected instead of being
// paid out with whatever token happens to be first in the config.
func (w *Worker) token(symbol string) (config.TokenConfig, bool) {
	t, ok := w.tokens[strings.ToUpper(symbol)]
	return t, ok
}

func (w *Worker) Run(ctx context.Context) error {
	if !config.Cfg.WithdrawServer.Enabled {
		logrus.Info("withdraw disabled")
		<-ctx.Done()
		return nil
	}
	interval := config.Duration(config.Cfg.WithdrawServer.PollInterval, 3*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := w.processCreated(ctx); err != nil {
			logrus.Error("withdraw process failed", ",err:", err)
		}

		if err := w.Reconcile(ctx); err != nil {
			logrus.Error("withdraw reconcile failed", ",err:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) processCreated(ctx context.Context) error {
	var rows []model.WithdrawRecord
	//从数据库找出订单状态为"created"的提现订单
	err := store.MyStore.DB.WithContext(ctx).
		Where("status = ?", model.WithdrawStateCreated).
		Order("id asc").Limit(20).Find(&rows).Error
	if err != nil {
		return err
	}
	for i := range rows {
		row := rows[i]
		err := w.execute(ctx, &row)
		// A halt already logged its own line with the attempt count, so it is
		// not repeated here every round.
		if err != nil && !errors.Is(err, ErrHalted) && !errors.Is(err, ErrHaltedFailed) {
			logrus.Error("withdraw execute failed ", ",order_no:", row.OrderNo, ",err:", err)
		}
	}
	return nil
}

// execute builds, signs and broadcasts one withdrawal. The state machine moves
// created -> signed -> broadcast with a compare-and-swap on every hop, so a
// duplicated worker cannot broadcast twice.
func (w *Worker) execute(ctx context.Context, row *model.WithdrawRecord) error {
	unlock, ok := store.MyStore.Lock(ctx, "withdraw:"+row.OrderNo, 5*time.Minute)
	if !ok {
		return nil
	}
	defer unlock()

	reason, err := w.riskCheck(ctx, row)
	if err != nil {
		// The order keeps its created state and is retried next round instead of
		// being rejected on a transient database failure.
		return err
	}
	if reason != "" {
		return w.reject(ctx, row, reason)
	}
	token, ok := w.token(row.Symbol)
	if !ok {
		return w.reject(ctx, row, "unsupported symbol "+row.Symbol)
	}
	amount, ok := new(big.Int).SetString(row.AmountUnits, 10)
	if !ok || amount.Sign() <= 0 {
		return w.reject(ctx, row, "invalid amount")
	}
	hot := config.Cfg.Wallet.HotWallet
	data, err := tron.EncodeTRC20Transfer(row.ToAddress, amount)
	if err != nil {
		return w.reject(ctx, row, "invalid destination")
	}

	// The hot wallet balance is checked before anything is rented or signed: a
	// transfer over the balance reverts on chain and still pays the fee, and the
	// order must not be failed for it either because the money is only missing
	// until finance refills the wallet.
	if err := w.checkBalance(ctx, row, token, hot.Address, amount); err != nil {
		return err
	}

	// The recipient's token state decides the energy tier: an address that
	// never held USDT needs roughly twice the energy, and underestimating
	// fails the transaction with OUT_OF_ENERGY while still paying the fee.
	need, err := w.mgr.EstimateEnergy(ctx, hot.Address, token.Contract, data)
	if err != nil {
		logrus.Warn("withdraw energy estimate failed, using worst case", ",err:", err)
		need = config.Cfg.Energy.EnergyPerTxNew
	}
	if !w.mgr.RentalEnabled() {
		// energy.rental_enabled=false: nothing is rented, the transfer pays its
		// own energy and bandwidth out of the hot wallet's TRX, so the TRX has to
		// be there before anything is signed.
		if err := w.checkBurnBudget(ctx, row, hot.Address, need); err != nil {
			return err
		}
	} else if w.pool == nil || !w.pool.HasEnergyFor(ctx, need) {
		requestID := fmt.Sprintf("withdraw-%d", row.ID)
		// Burning TRX is never an acceptable fallback here: it silently drains
		// the hot wallet's TRX at several times the rental price, so a rental
		// outage stops the withdrawal and alerts instead.
		if _, err := w.mgr.AcquireRented(ctx, "withdraw", hot.Address, need, requestID); err != nil {
			return w.halt(ctx, row, FailCodeEnergyRental,
				fmt.Sprintf("energy rental failed, withdrawal stopped (no TRX burn fallback): %v", err))
		}
	}

	tx, err := w.gw.BuildTRC20Transfer(ctx, hot.Address, token.Contract, data, config.Cfg.WithdrawServer.FeeLimitSun)
	if err != nil {
		return err
	}
	signed, err := w.sign.Sign(ctx, &signer.SignRequest{
		Purpose: signer.PurposeWithdraw,
		Path:    hot.Path,
		Address: hot.Address,
		Tx:      tx,
		Meta: signer.SignMeta{
			ToAddress:   row.ToAddress,
			Contract:    token.Contract,
			AmountUnits: row.AmountUnits,
		},
	})
	if err != nil {
		return err
	}
	expiry := time.Now().Add(time.Duration(w.expirationSeconds()) * time.Second)
	res := store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
		Where("id = ? AND status = ?", row.ID, model.WithdrawStateCreated).
		UpdateColumns(map[string]any{
			"status":       model.WithdrawStateSigned,
			"txid":         signed.TxID, //交易哈希
			"signed_raw":   signed.Tx.RawDataHex,
			"from_address": hot.Address,
			"expired_at":   expiry,
			"updated_at":   time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil // another worker took it
	}
	row.TxID, row.SignedRaw, row.FromAddress = signed.TxID, signed.Tx.RawDataHex, hot.Address
	return w.broadcast(ctx, row, signed.TxID, signed.Tx)
}

func (w *Worker) broadcast(ctx context.Context, row *model.WithdrawRecord, txid string, tx *tron.Transaction) error {
	id := row.ID
	result, err := w.gw.Broadcast(ctx, tx)
	now := time.Now()
	if err != nil {
		failCode, permanent := chain.FailNode, false
		if result != nil {
			failCode, permanent = chain.ClassifyBroadcast(result.Code, result.Message)
		}
		if permanent {
			// These bytes will never be accepted, so the order is settled now and
			// the business system can refund without waiting out the expiration.
			row.TxID, row.FailCode = txid, failCode
			if ferr := w.finish(ctx, *row, model.WithdrawStateFailed,
				"broadcast rejected: "+err.Error(), failCode, 0, 0, now); ferr != nil {
				return ferr
			}
			return err
		}
		// A transient broadcast error is not a failed withdrawal: the transaction
		// may already be propagating. Move to broadcast and let reconciliation
		// decide based on the txid.
		store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
			Where("id = ? AND status = ?", id, model.WithdrawStateSigned).
			UpdateColumns(map[string]any{
				"status": model.WithdrawStateBroadcast, "broadcast_at": now,
				"fail_reason": truncate(err.Error(), 240), "fail_code": failCode,
				"updated_at": now,
			})
		return err
	}
	store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
		Where("id = ? AND status = ?", id, model.WithdrawStateSigned).
		UpdateColumns(map[string]any{
			"status": model.WithdrawStateBroadcast, "txid": result.TxID,
			"broadcast_at": now, "fail_reason": "", "fail_code": "", "updated_at": now,
		})
	if w.pool != nil {
		w.pool.RecordUsage(1)
	}
	logrus.Info("withdraw broadcast", ",id:", id, ",txid:", txid, ",duplicated:", result.Duplicated)
	return nil
}

// checkBurnBudget verifies the hot wallet can pay this transfer's energy and
// bandwidth out of its own TRX. Signing without it would broadcast a transfer
// that reverts with OUT_OF_ENERGY and still charges whatever TRX was there, so
// the order is halted (funds intact, retried after a refill) instead.
func (w *Worker) checkBurnBudget(ctx context.Context, row *model.WithdrawRecord, from string, need int64) error {
	params, err := w.gw.GetChainParameters(ctx)
	if err != nil {
		return fmt.Errorf("withdraw: chain parameters query failed: %w", err)
	}
	res, err := w.gw.GetAccountResource(ctx, from)
	if err != nil {
		return fmt.Errorf("withdraw: hot wallet resource query failed: %w", err)
	}
	costSun := energy.BurnCostSun(res, params, need)
	if limit := config.Cfg.WithdrawServer.FeeLimitSun; limit > 0 && costSun > limit {
		return w.halt(ctx, row, FailCodeInsufficientTRX, fmt.Sprintf(
			"burning TRX for this transfer costs %d sun, above withdraw_server.fee_limit_sun=%d",
			costSun, limit))
	}
	balance, err := w.gw.GetTRXBalance(ctx, from)
	if err != nil {
		return fmt.Errorf("withdraw: hot wallet TRX balance query failed: %w", err)
	}
	if balance >= costSun {
		logrus.Info("withdraw pays its fee by burning TRX", ",order_no:", row.OrderNo,
			",energy_need:", need, ",cost_sun:", costSun, ",trx_balance_sun:", balance)
		return nil
	}
	return w.halt(ctx, row, FailCodeInsufficientTRX, fmt.Sprintf(
		"hot wallet TRX insufficient to burn the fee: balance=%d sun required=%d sun (energy=%d)",
		balance, costSun, need))
}

// checkBalance verifies the hot wallet still holds enough of the order's token
// for this order plus everything already signed or broadcast but not yet
// confirmed, since those transfers will settle out of the same balance.
func (w *Worker) checkBalance(ctx context.Context, row *model.WithdrawRecord, token config.TokenConfig, from string, amount *big.Int) error {
	balance, err := w.tokenBalance(ctx, token.Contract, from)
	if err != nil {
		// A node error is transient: the order stays created and is retried,
		// but nothing is signed on an unverified balance.
		return fmt.Errorf("withdraw: hot wallet balance query failed: %w", err)
	}
	inflight, err := w.inflightUnits(ctx, token.Contract, from)
	if err != nil {
		return err
	}
	needed := new(big.Int).Add(amount, inflight)
	if balance.Cmp(needed) >= 0 {
		return nil
	}
	return w.halt(ctx, row, FailCodeInsufficientBalance, fmt.Sprintf(
		"hot wallet %s balance insufficient: balance=%s required=%s (amount=%s in_flight=%s)",
		token.Symbol, balance.String(), needed.String(), amount.String(), inflight.String()))
}

// inflightUnits sums the orders of the same token already signed or broadcast
// from the address, whose amounts are still going to leave the wallet. Other
// tokens spend their own balance and must not be counted here.
func (w *Worker) inflightUnits(ctx context.Context, contract, from string) (*big.Int, error) {
	var sum string
	err := store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
		Select("COALESCE(SUM(amount_units),0)").
		Where("from_address = ? AND contract = ? AND status IN ?", from, contract,
			[]string{model.WithdrawStateSigned, model.WithdrawStateBroadcast}).
		Scan(&sum).Error
	if err != nil {
		return nil, fmt.Errorf("withdraw: sum in-flight withdrawals: %w", err)
	}
	units, ok := new(big.Int).SetString(sum, 10)
	if !ok {
		return nil, fmt.Errorf("withdraw: cannot parse in-flight sum %q", sum)
	}
	return units, nil
}

func (w *Worker) tokenBalance(ctx context.Context, contract, address string) (*big.Int, error) {
	data, err := tron.EncodeTRC20BalanceOf(address)
	if err != nil {
		return nil, err
	}
	out, _, err := w.gw.TriggerConstantContract(ctx, address, contract, data)
	if err != nil {
		return nil, err
	}
	value, ok := tron.ParseUint256(out)
	if !ok {
		return nil, fmt.Errorf("withdraw: cannot parse balance %q", out)
	}
	return value, nil
}

// halt records why the order did not move and alerts. The order keeps its
// created state on purpose: the funds are intact, so once the operator fixed
// the cause the same order is executed by a later round instead of being
// failed back to the business system.
//
// A cause nobody fixes must not halt the same order forever: every halt is
// counted on the order and once it reaches withdraw_server.halt_max_retries the
// order is failed with the same reason, which both stops the repeated alert and
// lets the business system refund the user.
// 每次停下都会累加次数，达到配置的上限后该订单结单为提现失败并把原因回调给业务系统。
func (w *Worker) halt(ctx context.Context, row *model.WithdrawRecord, failCode, reason string) error {
	attempt := row.HaltCount + 1
	limit := w.haltMaxRetries()
	if attempt >= limit {
		logrus.Error("ALERT withdraw halted, retry limit reached, failing the order ",
			",order_no:", row.OrderNo, ",fail_code:", failCode, ",attempt:", attempt,
			",halt_max_retries:", limit, ",reason:", reason)
		if err := w.failHalted(ctx, row, failCode, reason, attempt); err != nil {
			logrus.Error("withdraw halt settlement failed", ",order_no:", row.OrderNo, ",err:", err)
			return err
		}
		return fmt.Errorf("%w: %s", ErrHaltedFailed, reason)
	}
	logrus.Warn("withdraw halted ", ",order_no:", row.OrderNo, ",fail_code:", failCode,
		",attempt:", attempt, ",halt_max_retries:", limit, ",reason:", reason)
	if err := store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
		Where("id = ? AND status = ?", row.ID, model.WithdrawStateCreated).
		UpdateColumns(map[string]any{
			"fail_reason": truncate(reason, 240),
			"fail_code":   failCode,
			"halt_count":  attempt,
			"updated_at":  time.Now(),
		}).Error; err != nil {
		logrus.Error("withdraw halt bookkeeping failed", ",order_no:", row.OrderNo, ",err:", err)
	}
	row.HaltCount = attempt
	return fmt.Errorf("%w: %s (attempt %d/%d)", ErrHalted, reason, attempt, limit)
}

// failHalted settles an order that was halted halt_max_retries times: nothing
// was ever signed, so no transfer can be in flight and the order can be failed
// straight from created, with the halt reason reported to the business system.
func (w *Worker) failHalted(ctx context.Context, row *model.WithdrawRecord, failCode, reason string, attempt int) error {
	now := time.Now()
	failReason := fmt.Sprintf("%s (halted %d times, giving up)", reason, attempt)
	return store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.WithdrawRecord{}).
			Where("id = ? AND status = ?", row.ID, model.WithdrawStateCreated).
			UpdateColumns(map[string]any{
				"status":      model.WithdrawStateFailed,
				"fail_reason": truncate(failReason, 240),
				"fail_code":   failCode,
				"halt_count":  attempt,
				"updated_at":  now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // another worker moved it on
		}
		row.HaltCount, row.FailCode, row.Status = attempt, failCode, model.WithdrawStateFailed
		return store.EnqueueOutbox(tx, "withdraw:"+row.OrderNo, "withdraw_result",
			row.ExtParam, row.MerchantID, row.NotifyURL, w.event(row, "failed", failReason, now))
	})
}

// haltMaxRetries is how many halted rounds an order gets before it is failed.
func (w *Worker) haltMaxRetries() int {
	if config.Cfg.WithdrawServer.HaltMaxRetries > 0 {
		return config.Cfg.WithdrawServer.HaltMaxRetries
	}
	return DefaultHaltMaxRetries
}

// riskCheck applies the wallet-side safety limits. Business risk control lives
// in the business system; this is only the last line of defence.
// 风险防控
func (w *Worker) riskCheck(ctx context.Context, row *model.WithdrawRecord) (string, error) {
	if !tron.IsValidAddress(row.ToAddress) {
		return "invalid destination address", nil
	}
	// The blacklist lives in the address_blacklist table and is queried on every
	// check, so operator edits apply without restarting the worker.
	blacklisted, err := store.IsAddressBlacklisted(ctx, row.ToAddress)
	if err != nil {
		return "", fmt.Errorf("withdraw: query address blacklist: %w", err)
	}
	if blacklisted {
		return "destination address is blacklisted", nil
	}
	// Withdrawing to one of our own deposit addresses would be an internal
	// transfer with no user-visible effect; it must be handled off chain.
	var internal int64
	if err := store.MyStore.DB.WithContext(ctx).Model(&model.UserWallet{}).
		Where("address = ?", row.ToAddress).Count(&internal).Error; err == nil && internal > 0 {
		return "destination is an internal wallet address", nil
	}

	if err := store.IsInternalWallet(ctx, row, &internal); err == nil && internal > 0 {
		return "destination is an internal wallet address", nil
	}

	amount, ok := new(big.Int).SetString(row.AmountUnits, 10)
	if !ok || amount.Sign() <= 0 {
		return "invalid amount", nil
	}
	if maxUnits, ok := new(big.Int).SetString(config.Cfg.WithdrawServer.MaxAmountUnits, 10); ok && maxUnits.Sign() > 0 && amount.Cmp(maxUnits) > 0 {
		return "amount exceeds the single withdrawal limit", nil
	}
	if limit, ok := new(big.Int).SetString(config.Cfg.WithdrawServer.DailyMaxUnits, 10); ok && limit.Sign() > 0 {
		var sum string
		since := time.Now().Truncate(24 * time.Hour)
		store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
			Select("COALESCE(SUM(amount_units),0)").
			Where("created_at >= ? AND status NOT IN ?", since,
				[]string{model.WithdrawStateFailed, model.WithdrawStateRejected}).
			Scan(&sum)
		if today, ok := new(big.Int).SetString(sum, 10); ok {
			if new(big.Int).Add(today, amount).Cmp(limit) > 0 {
				return "daily withdrawal limit reached", nil
			}
		}
	}
	return "", nil
}

func (w *Worker) reject(ctx context.Context, row *model.WithdrawRecord, reason string) error {
	now := time.Now()
	row.FailCode = FailCodeRejected
	row.Status = model.WithdrawStateRejected
	return store.MyStore.DB.WithContext(ctx).Transaction(
		func(tx *gorm.DB) error {
			res := tx.Model(&model.WithdrawRecord{}).
				Where("id = ? AND status = ?", row.ID, model.WithdrawStateCreated).
				UpdateColumns(map[string]any{
					"status": model.WithdrawStateRejected, "fail_reason": reason,
					"fail_code": FailCodeRejected, "updated_at": now,
				})
			if res.Error != nil || res.RowsAffected == 0 {
				return res.Error
			}
			logrus.Warn("withdraw rejected", ",order_no:", row.OrderNo, ",reason:", reason)
			return store.EnqueueOutbox(tx, "withdraw:"+row.OrderNo, "withdraw_result",
				row.ExtParam, row.MerchantID, row.NotifyURL, w.event(row, "rejected", reason, now))
		},
	)
}

// Reconcile settles broadcast withdrawals and notifies the business system exactly once per order.
// 对账结算广播提款，并按订单向业务系统发送一次通知。
func (w *Worker) Reconcile(ctx context.Context) error {
	var rows []model.WithdrawRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("status IN ?", []string{model.WithdrawStateSigned, model.WithdrawStateBroadcast}).
		Order("id asc").Limit(100).Find(&rows).Error
	if err != nil {
		return err
	}
	var head int64
	if !w.gw.SolidityConfirm() {
		// Only the confirm_blocks fallback counts depth from the head; with a
		// solidity path the node itself decides what is irreversible.
		block, err := w.gw.GetNowBlock(ctx)
		if err != nil {
			return err
		}
		head = block.Number()
	}
	for i := range rows {
		row := rows[i]
		if err := w.reconcileOne(ctx, row, head); err != nil {
			logrus.Error("reconcile withdraw failed", ",order_no:", row.OrderNo, ",err:", err)
		}
	}
	return nil
}

func (w *Worker) reconcileOne(ctx context.Context, row model.WithdrawRecord, head int64) error {
	if row.TxID == "" {
		return nil
	}
	info, err := w.gw.GetTxInfoByID(ctx, row.TxID)
	if err != nil {
		return err
	}
	now := time.Now()
	if info == nil {
		// Not on chain. While the signed transaction is still valid the only
		// safe action is to rebroadcast the very same bytes.
		if row.ExpiredAt != nil && now.Before(*row.ExpiredAt) {
			if row.SignedRaw != "" {
				return w.rebroadcast(ctx, row)
			}
			return nil
		}
		// Expired and absent from the chain: it can never be included now, so
		// the order is failed and the business system refunds the user.
		return w.finish(ctx, row, model.WithdrawStateFailed, "transaction expired without inclusion",
			chain.FailExpired, 0, 0, now)
	}
	// The outcome is only reported once it can no longer change: an
	// unsolidified receipt still belongs to a block a fork may replace, and the
	// very same signed bytes could then be included again with a different
	// result.
	final, err := w.finalInfo(ctx, row.TxID, head)
	if err != nil {
		return err
	}
	if final == nil {
		return nil // on chain, not irreversible yet
	}
	if !final.Succeeded() {
		reason := final.FailureReason()
		failCode := chain.ClassifyReceipt(final)
		// The USDT never left the hot wallet, so the order is reported failed and
		// the business system refunds; withdrawals are never retried on chain
		// because a second transfer could double pay one business order.
		logrus.Error("withdraw failed on chain", ",order_no:", row.OrderNo,
			",txid:", row.TxID, ",fail_code:", failCode, ",reason:", reason)
		return w.finish(ctx, row, model.WithdrawStateFailed, "on-chain failure: "+reason,
			failCode, final.Receipt.EnergyUsageTotal, final.Fee, now)
	}
	// A SUCCESS receipt is not proof that the tokens moved: a receipt carrying no
	// event at all, or a token that returns false instead of reverting, leaves the
	// balance untouched while the transaction itself succeeds. Reporting that as
	// paid would credit the user with a transfer that never happened.
	if !transferred(final, row) {
		logrus.Error("withdraw receipt succeeded without a matching Transfer event",
			",order_no:", row.OrderNo, ",txid:", row.TxID, ",to_address:", row.ToAddress,
			",amount_units:", row.AmountUnits, ",logs:", len(final.Log))
		return w.finish(ctx, row, model.WithdrawStateFailed,
			"receipt succeeded but no matching Transfer event was emitted",
			chain.FailNoTransfer, final.Receipt.EnergyUsageTotal, final.Fee, now)
	}
	return w.finish(ctx, row, model.WithdrawStateConfirmed, "", "", final.Receipt.EnergyUsageTotal, final.Fee, now)
}

// transferred reports whether the receipt contains the TRC20 Transfer this order
// was supposed to produce: the order's token contract, its own hot wallet as
// sender, its destination and its exact amount.
func transferred(info *chain.TxInfo, row model.WithdrawRecord) bool {
	amount, ok := new(big.Int).SetString(row.AmountUnits, 10)
	if !ok {
		return false
	}
	for _, lg := range info.Log {
		if len(lg.Topics) != 3 || !strings.EqualFold(lg.Topics[0], tron.TransferEventTopic) {
			continue
		}
		contract, err := tron.HexToAddress(lg.Address)
		if err != nil || contract != row.Contract {
			continue
		}
		from, err := tron.HexToAddress(lg.Topics[1])
		// from_address is written when the order is created, but an order from an
		// older schema may not have it; the sender is then not checked.
		if err != nil || (row.FromAddress != "" && from != row.FromAddress) {
			continue
		}
		to, err := tron.HexToAddress(lg.Topics[2])
		if err != nil || to != row.ToAddress {
			continue
		}
		if value, ok := tron.ParseUint256(lg.Data); ok && value.Cmp(amount) == 0 {
			return true
		}
	}
	return false
}

// finalInfo returns the receipt the settlement decision may be based on, or nil
// while the transaction is not final yet.
//
// With chain.solidity_for_confirm the receipt is read from the solidity node,
// which only serves solidified (irreversible) blocks, so finality is the node's
// consensus answer instead of a block count that ignores whether the block is
// still on the main chain. withdraw_server.confirm_blocks only applies to a
// deployment without a solidity path, where counting depth from the head is the
// only available approximation.
func (w *Worker) finalInfo(ctx context.Context, txid string, head int64) (*chain.TxInfo, error) {
	if w.gw.SolidityConfirm() {
		return w.gw.GetTxInfoByIDConfirmed(ctx, txid)
	}
	info, err := w.gw.GetTxInfoByID(ctx, txid)
	if err != nil || info == nil {
		return nil, err
	}
	if head-info.BlockNumber < w.confirmBlocks() {
		return nil, nil
	}
	return info, nil
}

func (w *Worker) rebroadcast(ctx context.Context, row model.WithdrawRecord) error {
	tx := &tron.Transaction{TxID: row.TxID, RawDataHex: row.SignedRaw}
	// The signature is not stored separately: sign-service is asked to sign the
	// same raw data again, which is deterministic for the same key and payload.
	signed, err := w.sign.Sign(ctx, &signer.SignRequest{
		Purpose: signer.PurposeWithdraw,
		Path:    config.Cfg.Wallet.HotWallet.Path,
		Address: config.Cfg.Wallet.HotWallet.Address,
		Tx:      tx,
		Meta: signer.SignMeta{
			ToAddress:   row.ToAddress,
			Contract:    row.Contract,
			AmountUnits: row.AmountUnits,
		},
	})
	if err != nil {
		return err
	}
	if signed.TxID != row.TxID {
		return fmt.Errorf("withdraw: refusing to rebroadcast, txid changed from %s to %s", row.TxID, signed.TxID)
	}
	res, err := w.gw.Broadcast(ctx, signed.Tx)
	if err == nil {
		return nil
	}
	// A permanent rejection cannot become valid, so the order is settled now
	// instead of being rebroadcast every round until it expires.
	if res != nil {
		if failCode, permanent := chain.ClassifyBroadcast(res.Code, res.Message); permanent {
			if ferr := w.finish(ctx, row, model.WithdrawStateFailed,
				"broadcast rejected: "+err.Error(), failCode, 0, 0, time.Now()); ferr != nil {
				return ferr
			}
		}
	}
	return err
}

func (w *Worker) finish(ctx context.Context, row model.WithdrawRecord, status, reason, failCode string, energyUsed, fee int64, now time.Time) error {
	if status != model.WithdrawStateConfirmed {
		// An operator reading a failed order has to see why it failed, so neither
		// column is ever left empty.
		if reason == "" {
			reason = "settled as " + status + " without a reported reason"
		}
		if failCode == "" {
			failCode = chain.FailUnknown
		}
	}
	row.FailCode = failCode
	row.Status = status
	return store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"status":      status,
			"fail_reason": truncate(reason, 240),
			"fail_code":   failCode,
			"energy_used": energyUsed,
			"fee_sun":     fee,
			"updated_at":  now,
		}
		if status == model.WithdrawStateConfirmed {
			updates["confirmed_at"] = now
		}
		res := tx.Model(&model.WithdrawRecord{}).
			Where("id = ? AND status IN ?", row.ID,
				[]string{model.WithdrawStateSigned, model.WithdrawStateBroadcast}).
			UpdateColumns(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		outcome := "success"
		if status != model.WithdrawStateConfirmed {
			outcome = "failed"
		}
		// The business system is told the outcome of every settled order, success or
		// failure, on the notify_url it submitted with the order.
		return store.EnqueueOutbox(tx, "withdraw:"+row.OrderNo, "withdraw_result", row.ExtParam, row.MerchantID, row.NotifyURL,
			w.event(&row, outcome, reason, now))
	})
}

func (w *Worker) event(row *model.WithdrawRecord, outcome, reason string, now time.Time) map[string]any {
	return map[string]any{
		"event_id":   "withdraw:" + row.OrderNo,
		"type":       "withdraw_result",
		"order_no":   row.OrderNo,
		"trade_no":   row.TradeNo,
		"status":     row.Status,
		"ext_param":  row.ExtParam,
		"chain":      row.Chain,
		"symbol":     row.Symbol,
		"amount":     row.AmountUnits,
		"decimals":   row.Decimals,
		"to_address": row.ToAddress,
		"txid":       row.TxID,
		"result":     outcome,
		"reason":     reason,
		// fail_code lets the business system branch without parsing reason.
		"fail_code":   row.FailCode,
		"finished_at": now.Unix(),
	}
}

func (w *Worker) confirmBlocks() int64 {
	if config.Cfg.WithdrawServer.ConfirmBlocks > 0 {
		return config.Cfg.WithdrawServer.ConfirmBlocks
	}
	return config.Cfg.Deposit.Confirmations
}

func (w *Worker) expirationSeconds() int64 {
	if config.Cfg.WithdrawServer.TxExpirationSec > 0 {
		return config.Cfg.WithdrawServer.TxExpirationSec
	}
	return 60
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
