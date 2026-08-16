package energy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/sirupsen/logrus"
)

// Topup refills the prepaid balance of each rental provider from a dedicated
// small gas account.
//
// This is an automated outbound money path, so it is treated like a withdrawal:
//   - the master switch (energy.auto_topup.enabled) defaults to off and only
//     alerting happens while it is off;
//   - the destination is re-read from the provider API before every transfer
//     and must match the hard coded whitelist, otherwise a compromised provider
//     API could redirect funds;
//   - per transfer, per day and per day count caps stop the loop from draining
//     the gas account;
//   - only one refill runs at a time, and a previous unsettled refill blocks the
//     next one, because provider crediting lags the on-chain confirmation.
type Topup struct {
	cfg     config.AutoTopupConfig
	gw      *chain.Gateway
	signer  *signer.Client
	provs   map[string]Provider
	gasPath string

	//st     *store.Store
	//log     *logrus.Logger
}

func NewTopup(cfg config.AutoTopupConfig, st *store.Store, gw *chain.Gateway, sign *signer.Client, provs map[string]Provider, gasPath string) *Topup {
	return &Topup{cfg: cfg, gw: gw, signer: sign, provs: provs, gasPath: gasPath}
}

func (t *Topup) Run(ctx context.Context) error {
	interval := config.Duration(t.cfg.CheckInterval, 5*time.Minute)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := t.check(ctx); err != nil {
			logrus.Error("provider balance check failed", ",err:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (t *Topup) check(ctx context.Context) error {
	t.checkGasAccount(ctx)
	for name, prov := range t.provs {
		conf, ok := t.cfg.Providers[name]
		if !ok || !conf.Enabled {
			continue
		}
		balance, depositAddr, err := prov.Balance(ctx)
		if err != nil {
			logrus.Error("provider balance query failed", ",provider:", name, ",err:", err)
			continue
		}
		if balance >= conf.LowWatermarkTRX {
			continue
		}
		if !t.cfg.Enabled {
			// Alert only: the switch is the operator's kill switch.
			logrus.Warn("provider prepaid balance low, auto topup disabled",
				",provider:", name, ",balance_trx:", balance, ",low_watermark_trx:", conf.LowWatermarkTRX)
			continue
		}
		if err := t.refill(ctx, name, conf, balance, depositAddr); err != nil {
			logrus.Error("provider topup failed", ",provider:", name, ",err:", err)
		}
	}
	return nil
}

// checkGasAccount only alerts: the gas account is refilled manually from the
// finance cold wallet, otherwise the cold wallet would be back on an automated
// path.
func (t *Topup) checkGasAccount(ctx context.Context) {
	if t.cfg.SourceAddress == "" {
		return
	}
	sun, err := t.gw.GetTRXBalance(ctx, t.cfg.SourceAddress)
	if err != nil {
		logrus.Error("gas account balance query failed", ",err:", err)
		return
	}
	trx := float64(sun) / 1e6
	if t.cfg.GasAccount.LowWatermarkTRX > 0 && trx < t.cfg.GasAccount.LowWatermarkTRX {
		logrus.Warn("gas account balance low, manual refill from the finance wallet required",
			",balance_trx:", trx, ",low_watermark_trx:", t.cfg.GasAccount.LowWatermarkTRX,
			",target_trx:", t.cfg.GasAccount.TargetTRX)
	}
}

func (t *Topup) refill(ctx context.Context, provider string, conf config.ProviderTopupConf, balance float64, reportedDeposit string) error {
	unlock, ok := store.MyStore.Lock(ctx, "topup:"+provider, 10*time.Minute)
	if !ok {
		return nil
	}
	defer unlock()

	// The provider reported address must match the configured whitelist.
	if reportedDeposit != "" && reportedDeposit != conf.DepositAddressUrl {
		return fmt.Errorf("provider %s reported deposit address %s which is not the whitelisted %s: refusing to transfer",
			provider, reportedDeposit, conf.DepositAddressUrl)
	}
	if err := t.guardPending(ctx, provider); err != nil {
		return err
	}
	amount := conf.TargetTRX - balance
	if amount <= 0 {
		return nil
	}
	if conf.MaxSingleTopupTRX > 0 && amount > conf.MaxSingleTopupTRX {
		amount = conf.MaxSingleTopupTRX
	}
	if err := t.guardDailyLimits(ctx, provider, conf, amount); err != nil {
		return err
	}

	requestID := fmt.Sprintf("topup-%s-%d", provider, time.Now().UnixMilli())
	amountSun := int64(amount * 1e6)
	row := &model.TopupRecord{
		Provider:          provider,
		RequestID:         requestID,
		FromAddress:       t.cfg.SourceAddress,
		ToAddress:         conf.DepositAddressUrl,
		AmountTRX:         amount,
		Amount:            model.FormatTRX(amount),
		TriggerBalanceTRX: balance,
		Status:            model.TopupStateCreated,
		Operator:          "auto",
	}
	if err := store.MyStore.DB.WithContext(ctx).Create(row).Error; err != nil {
		return err
	}

	tx, err := t.gw.BuildTRXTransfer(ctx, t.cfg.SourceAddress, conf.DepositAddressUrl, amountSun)
	if err != nil {
		t.fail(ctx, row, err)
		return err
	}
	signed, err := t.signer.Sign(ctx, &signer.SignRequest{
		Purpose: signer.PurposeTopup,
		Path:    t.gasPath,
		Address: t.cfg.SourceAddress,
		Tx:      tx,
		Meta:    signer.SignMeta{ToAddress: conf.DepositAddressUrl, AmountSun: amountSun},
	})
	if err != nil {
		t.fail(ctx, row, err)
		return err
	}
	res, err := t.gw.Broadcast(ctx, signed.Tx)
	if err != nil {
		// The transaction may still be on chain; leave the row broadcast and
		// let reconciliation settle it rather than sending a second transfer.
		store.MyStore.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
			"txid": signed.TxID, "status": model.TopupStateBroadcast,
			"fail_reason": truncate(err.Error(), 240), "updated_at": time.Now(),
		})
		return err
	}
	store.MyStore.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
		"txid": res.TxID, "status": model.TopupStateBroadcast, "updated_at": time.Now(),
	})
	logrus.Warn("provider prepaid balance topped up automatically",
		",provider:", provider, ",amount_trx:", amount, ",balance_trx:", balance, ",txid:", res.TxID)
	return nil
}

// guardPending refuses to start a refill while a previous one has not been
// credited: provider crediting lags, so an eager retry double pays.
func (t *Topup) guardPending(ctx context.Context, provider string) error {
	var pending model.TopupRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("provider = ? AND status IN ?", provider,
			[]string{model.TopupStateCreated, model.TopupStateBroadcast, model.TopupStateConfirmed}).
		Order("id desc").Take(&pending).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("provider %s already has an unsettled topup (%s, status=%s)", provider, pending.RequestID, pending.Status)
}

func (t *Topup) guardDailyLimits(ctx context.Context, provider string, conf config.ProviderTopupConf, amount float64) error {
	since := time.Now().Truncate(24 * time.Hour)
	var agg struct {
		Total float64
		Count int64
	}
	err := store.MyStore.DB.WithContext(ctx).Model(&model.TopupRecord{}).
		Select("COALESCE(SUM(amount_trx),0) as total, COUNT(*) as count").
		Where("provider = ? AND created_at >= ? AND status <> ?", provider, since, model.TopupStateFailed).
		Scan(&agg).Error
	if err != nil {
		return err
	}
	if conf.MaxDailyTopupCount > 0 && agg.Count >= int64(conf.MaxDailyTopupCount) {
		return fmt.Errorf("provider %s hit the daily topup count cap (%d), switching to manual", provider, conf.MaxDailyTopupCount)
	}
	if conf.MaxDailyTopupTRX > 0 && agg.Total+amount > conf.MaxDailyTopupTRX {
		return fmt.Errorf("provider %s would exceed the daily topup cap (%.0f + %.0f > %.0f TRX), switching to manual",
			provider, agg.Total, amount, conf.MaxDailyTopupTRX)
	}
	return nil
}

func (t *Topup) fail(ctx context.Context, row *model.TopupRecord, cause error) {
	store.MyStore.DB.WithContext(ctx).Model(row).UpdateColumns(map[string]any{
		"status": model.TopupStateFailed, "fail_reason": truncate(cause.Error(), 240), "updated_at": time.Now(),
	})
}

// Reconcile settles broadcast refills: it confirms them on chain and then waits
// for the provider balance to actually reflect the transfer.
func (t *Topup) Reconcile(ctx context.Context) error {
	var rows []model.TopupRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("status IN ?", []string{model.TopupStateBroadcast, model.TopupStateConfirmed}).
		Order("id asc").Limit(50).Find(&rows).Error
	if err != nil {
		return err
	}
	for i := range rows {
		row := rows[i]
		if row.TxID == "" {
			continue
		}
		info, err := t.gw.GetTxInfoByID(ctx, row.TxID)
		if err != nil {
			continue
		}
		now := time.Now()
		if info == nil {
			continue
		}
		if !info.Succeeded() {
			store.MyStore.DB.WithContext(ctx).Model(&model.TopupRecord{}).Where("id = ?", row.ID).
				UpdateColumns(map[string]any{"status": model.TopupStateFailed, "fail_reason": "on-chain failure", "updated_at": now})
			continue
		}
		if row.Status == model.TopupStateBroadcast {
			store.MyStore.DB.WithContext(ctx).Model(&model.TopupRecord{}).Where("id = ?", row.ID).
				UpdateColumns(map[string]any{"status": model.TopupStateConfirmed, "confirmed_at": now, "updated_at": now})
		}
		prov, ok := t.provs[row.Provider]
		if !ok {
			continue
		}
		balance, _, err := prov.Balance(ctx)
		if err != nil {
			continue
		}
		if balance >= row.TriggerBalanceTRX+row.AmountTRX*0.9 {
			store.MyStore.DB.WithContext(ctx).Model(&model.TopupRecord{}).Where("id = ?", row.ID).
				UpdateColumns(map[string]any{"status": model.TopupStateCredited, "updated_at": now})
		}
	}
	return nil
}
