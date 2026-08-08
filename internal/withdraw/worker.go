// Package withdraw executes withdrawal orders submitted by the business
// system. The business system already debited or froze the user balance, so the
// only guarantee this package must provide is: one biz_order_no results in at
// most one on-chain transfer, and its final outcome is always reported.
package withdraw

import (
	"context"
	"errors"
	"fmt"
	"math/big"
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

type Worker struct {
	//cfg  *config.Config
	//st   *store.Store
	gw   *chain.Gateway
	sign *signer.Client
	mgr  *energy.Manager
	pool *energy.Pool
	//log   *logrus.Logger
	token config.TokenConfig
}

func New(st *store.Store, gw *chain.Gateway, sign *signer.Client, mgr *energy.Manager, pool *energy.Pool, log *logrus.Logger) (*Worker, error) {
	var token config.TokenConfig
	for _, t := range config.Cfg.Wallet.Tokens {
		if t.Enabled {
			token = t
			break
		}
	}
	if token.Contract == "" {
		return nil, errors.New("withdraw: no enabled token configured")
	}
	if config.Cfg.Wallet.HotWallet.Address == "" || config.Cfg.Wallet.HotWallet.Path == "" {
		return nil, errors.New("withdraw: wallet.hot_wallet address and path are required")
	}
	return &Worker{gw: gw, sign: sign, mgr: mgr, pool: pool, //log: log,
		token: token}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	if !config.Cfg.Withdraw.Enabled {
		logrus.Info("withdraw disabled")
		<-ctx.Done()
		return nil
	}
	interval := config.Duration(config.Cfg.Withdraw.PollInterval, 3*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := w.processCreated(ctx); err != nil {
			logrus.Error("withdraw process failed", "err", err)
		}
		if err := w.Reconcile(ctx); err != nil {
			logrus.Error("withdraw reconcile failed", "err", err)
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
	err := store.MyStore.DB.WithContext(ctx).
		Where("status = ?", model.WithdrawStateCreated).
		Order("id asc").Limit(20).Find(&rows).Error
	if err != nil {
		return err
	}
	for i := range rows {
		row := rows[i]
		if err := w.execute(ctx, &row); err != nil {
			logrus.Error("withdraw execute failed", "biz_order_no", row.BizOrderNo, "err", err)
		}
	}
	return nil
}

// execute builds, signs and broadcasts one withdrawal. The state machine moves
// created -> signed -> broadcast with a compare-and-swap on every hop, so a
// duplicated worker cannot broadcast twice.
func (w *Worker) execute(ctx context.Context, row *model.WithdrawRecord) error {
	unlock, ok := store.MyStore.Lock(ctx, "withdraw:"+row.BizOrderNo, 5*time.Minute)
	if !ok {
		return nil
	}
	defer unlock()

	if reason := w.riskCheck(ctx, row); reason != "" {
		return w.reject(ctx, row, reason)
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

	// The recipient's token state decides the energy tier: an address that
	// never held USDT needs roughly twice the energy, and underestimating
	// fails the transaction with OUT_OF_ENERGY while still paying the fee.
	need, err := w.mgr.EstimateEnergy(ctx, hot.Address, w.token.Contract, data)
	if err != nil {
		logrus.Warn("withdraw energy estimate failed, using worst case", "err", err)
		need = config.Cfg.Energy.EnergyPerTxNew
	}
	if w.pool != nil && !w.pool.HasEnergyFor(ctx, need) {
		requestID := fmt.Sprintf("withdraw-%d", row.ID)
		if _, err := w.mgr.Acquire(ctx, "hot_pool", hot.Address, need, requestID); err != nil {
			// Not fatal: the transaction burns TRX instead of stalling.
			logrus.Error("withdraw energy rental failed, falling back to burning TRX", "err", err)
		}
	}

	tx, err := w.gw.BuildTRC20Transfer(ctx, hot.Address, w.token.Contract, data, config.Cfg.Withdraw.FeeLimitSun)
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
			Contract:    w.token.Contract,
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
			"txid":         signed.TxID,
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
	return w.broadcast(ctx, row.ID, signed.TxID, signed.Tx)
}

func (w *Worker) broadcast(ctx context.Context, id int64, txid string, tx *tron.Transaction) error {
	result, err := w.gw.Broadcast(ctx, tx)
	now := time.Now()
	if err != nil {
		// A broadcast error is not a failed withdrawal: the transaction may
		// already be propagating. Move to broadcast and let reconciliation
		// decide based on the txid.
		store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
			Where("id = ? AND status = ?", id, model.WithdrawStateSigned).
			UpdateColumns(map[string]any{
				"status": model.WithdrawStateBroadcast, "broadcast_at": now,
				"fail_reason": truncate(err.Error(), 240), "updated_at": now,
			})
		return err
	}
	store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
		Where("id = ? AND status = ?", id, model.WithdrawStateSigned).
		UpdateColumns(map[string]any{
			"status": model.WithdrawStateBroadcast, "txid": result.TxID,
			"broadcast_at": now, "fail_reason": "", "updated_at": now,
		})
	if w.pool != nil {
		w.pool.RecordUsage(1)
	}
	logrus.Info("withdraw broadcast", "id", id, "txid", txid, "duplicated", result.Duplicated)
	return nil
}

// riskCheck applies the wallet-side safety limits. Business risk control lives
// in the business system; this is only the last line of defence.
func (w *Worker) riskCheck(ctx context.Context, row *model.WithdrawRecord) string {
	if !tron.IsValidAddress(row.ToAddress) {
		return "invalid destination address"
	}
	for _, banned := range config.Cfg.Withdraw.AddressBlacklist {
		if banned == row.ToAddress {
			return "destination address is blacklisted"
		}
	}
	// Withdrawing to one of our own deposit addresses would be an internal
	// transfer with no user-visible effect; it must be handled off chain.
	var internal int64
	if err := store.MyStore.DB.WithContext(ctx).Model(&model.Wallet{}).
		Where("address = ?", row.ToAddress).Count(&internal).Error; err == nil && internal > 0 {
		return "destination is an internal wallet address"
	}
	amount, ok := new(big.Int).SetString(row.AmountUnits, 10)
	if !ok || amount.Sign() <= 0 {
		return "invalid amount"
	}
	if maxUnits, ok := new(big.Int).SetString(config.Cfg.Withdraw.MaxAmountUnits, 10); ok && maxUnits.Sign() > 0 && amount.Cmp(maxUnits) > 0 {
		return "amount exceeds the single withdrawal limit"
	}
	if limit, ok := new(big.Int).SetString(config.Cfg.Withdraw.DailyMaxUnits, 10); ok && limit.Sign() > 0 {
		var sum string
		since := time.Now().Truncate(24 * time.Hour)
		store.MyStore.DB.WithContext(ctx).Model(&model.WithdrawRecord{}).
			Select("COALESCE(SUM(amount_units),0)").
			Where("created_at >= ? AND status NOT IN ?", since,
				[]string{model.WithdrawStateFailed, model.WithdrawStateRejected}).
			Scan(&sum)
		if today, ok := new(big.Int).SetString(sum, 10); ok {
			if new(big.Int).Add(today, amount).Cmp(limit) > 0 {
				return "daily withdrawal limit reached"
			}
		}
	}
	return ""
}

func (w *Worker) reject(ctx context.Context, row *model.WithdrawRecord, reason string) error {
	now := time.Now()
	return store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.WithdrawRecord{}).
			Where("id = ? AND status = ?", row.ID, model.WithdrawStateCreated).
			UpdateColumns(map[string]any{
				"status": model.WithdrawStateRejected, "fail_reason": reason, "updated_at": now,
			})
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		logrus.Warn("withdraw rejected", "biz_order_no", row.BizOrderNo, "reason", reason)
		return store.EnqueueOutbox(tx, "withdraw:"+row.BizOrderNo, "withdraw_result", w.event(row, "rejected", reason, now))
	})
}

// Reconcile settles broadcast withdrawals and notifies the business system
// exactly once per order.
func (w *Worker) Reconcile(ctx context.Context) error {
	var rows []model.WithdrawRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("status IN ?", []string{model.WithdrawStateSigned, model.WithdrawStateBroadcast}).
		Order("id asc").Limit(100).Find(&rows).Error
	if err != nil {
		return err
	}
	head, err := w.gw.GetNowBlock(ctx)
	if err != nil {
		return err
	}
	for i := range rows {
		row := rows[i]
		if err := w.reconcileOne(ctx, row, head.Number()); err != nil {
			logrus.Error("reconcile withdraw failed", "biz_order_no", row.BizOrderNo, "err", err)
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
		return w.finish(ctx, row, model.WithdrawStateFailed, "transaction expired without inclusion", 0, 0, now)
	}
	if !info.Succeeded() {
		reason := info.Receipt.Result
		if reason == "" {
			reason = info.ResMessage
		}
		return w.finish(ctx, row, model.WithdrawStateFailed, "on-chain failure: "+reason,
			info.Receipt.EnergyUsageTotal, info.Fee, now)
	}
	if head-info.BlockNumber < w.confirmBlocks() {
		return nil
	}
	return w.finish(ctx, row, model.WithdrawStateConfirmed, "", info.Receipt.EnergyUsageTotal, info.Fee, now)
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
	_, err = w.gw.Broadcast(ctx, signed.Tx)
	return err
}

func (w *Worker) finish(ctx context.Context, row model.WithdrawRecord, status, reason string, energyUsed, fee int64, now time.Time) error {
	return store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"status":      status,
			"fail_reason": truncate(reason, 240),
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
		return store.EnqueueOutbox(tx, "withdraw:"+row.BizOrderNo, "withdraw_result", w.event(&row, outcome, reason, now))
	})
}

func (w *Worker) event(row *model.WithdrawRecord, outcome, reason string, now time.Time) map[string]any {
	return map[string]any{
		"event_id":     "withdraw:" + row.BizOrderNo,
		"type":         "withdraw_result",
		"biz_order_no": row.BizOrderNo,
		"uid":          row.UID,
		"chain":        row.Chain,
		"symbol":       row.Symbol,
		"amount":       row.AmountUnits,
		"decimals":     row.Decimals,
		"to_address":   row.ToAddress,
		"txid":         row.TxID,
		"result":       outcome,
		"reason":       reason,
		"finished_at":  now.Unix(),
	}
}

func (w *Worker) confirmBlocks() int64 {
	if config.Cfg.Withdraw.ConfirmBlocks > 0 {
		return config.Cfg.Withdraw.ConfirmBlocks
	}
	return config.Cfg.Deposit.Confirmations
}

func (w *Worker) expirationSeconds() int64 {
	if config.Cfg.Withdraw.TxExpirationSec > 0 {
		return config.Cfg.Withdraw.TxExpirationSec
	}
	return 60
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
