// Package sweep moves USDT from user deposit addresses into the finance wallet.
//
// Each user address is its own account, so each sweep needs its own delegated
// energy. To avoid paying for a rental that expires unused, the transaction is
// built and signed *before* the energy is ordered, and it is broadcast as soon
// as the delegation is confirmed on chain.
package sweep

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/hongkongstar6/trc20/internal/tron"
	"github.com/sirupsen/logrus"
)

type Service struct {
	//cfg    *config.Config
	//st     *store.Store
	gw     *chain.Gateway
	sign   *signer.Client
	mgr    *energy.Manager
	pricer *energy.Pricer
	log    *logrus.Logger
	token  config.TokenConfig
}

func New(gw *chain.Gateway, sign *signer.Client, mgr *energy.Manager, pricer *energy.Pricer) (*Service, error) {
	var token config.TokenConfig
	for _, t := range config.Cfg.Wallet.Tokens {
		if t.Enabled {
			token = t
			break
		}
	}
	if token.Contract == "" {
		return nil, errors.New("sweep: no enabled token configured")
	}
	if config.Cfg.Wallet.FinanceWallet.Address == "" {
		return nil, errors.New("sweep: wallet.finance_wallet.address is required")
	}
	return &Service{gw: gw, sign: sign, mgr: mgr, pricer: pricer, token: token, log: logrus.StandardLogger()}, nil
}

func (s *Service) Run(ctx context.Context) error {
	if !config.Cfg.SweepServer.Enabled {
		s.log.Info("sweep disabled")
		<-ctx.Done()
		return nil
	}
	interval := config.Duration(config.Cfg.SweepServer.Interval, 5*time.Minute)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.round(ctx); err != nil {
			s.log.Error("sweep round failed", ",err:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// round picks candidate addresses and sweeps them one by one.
func (s *Service) round(ctx context.Context) error {
	candidates, err := s.candidates(ctx)
	if err != nil {
		return err
	}
	minUnits := s.minSweepUnits()
	maxSkips := config.Cfg.SweepServer.Threshold.MaxSkipRounds
	for _, addr := range candidates {
		balance, err := s.tokenBalance(ctx, addr.Address)
		if err != nil {
			s.log.Error("balance query failed", "address", addr.Address, ",err:", err)
			continue
		}
		if balance.Sign() <= 0 {
			continue
		}
		// Above the threshold the very first round already sweeps; below it the
		// address waits until it was skipped max_skip_rounds times (or went
		// stale), so small balances still reach the finance wallet eventually.
		if balance.Cmp(minUnits) < 0 {
			skips, err := s.recordSkip(ctx, addr.Address)
			if err != nil {
				s.log.Error("sweep skip counter failed", "address", addr.Address, ",err:", err)
				continue
			}
			forced := maxSkips > 0 && skips >= maxSkips
			if !forced && !s.isStale(ctx, addr.Address) {
				continue
			}
			s.log.Info("sweeping below-threshold address", "address", addr.Address,
				"balance", balance.String(), "min_units", minUnits.String(), "skips", skips)
		}
		if err := s.sweepOne(ctx, addr, balance); err != nil {
			s.log.Error("sweep failed", "address", addr.Address, ",err:", err)
		}
	}
	return nil
}

// recordSkip counts one more skipped round for the address and returns the new
// total. The counter is cleared once a sweep of the address is confirmed.
func (s *Service) recordSkip(ctx context.Context, address string) (int, error) {
	now := time.Now()
	err := store.MyStore.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "address"}},
		DoUpdates: clause.Assignments(map[string]any{
			"skip_count":   gorm.Expr("skip_count + 1"),
			"last_skip_at": now,
			"updated_at":   now,
		}),
	}).Create(&model.SweepSkip{Address: address, SkipCount: 1, LastSkipAt: now}).Error
	if err != nil {
		return 0, err
	}
	var row model.SweepSkip
	if err := store.MyStore.DB.WithContext(ctx).Where("address = ?", address).Take(&row).Error; err != nil {
		return 0, err
	}
	return row.SkipCount, nil
}

// candidates are deposit addresses with confirmed unswept deposits.
func (s *Service) candidates(ctx context.Context) ([]model.UserWallet, error) {
	var addresses []string
	err := store.MyStore.DB.WithContext(ctx).Model(&model.DepositRecord{}).
		Distinct("to_address").
		Where("status = ? AND swept = ? AND internal = ?", model.DepositStateConfirmed, false, false).
		Limit(config.Cfg.SweepServer.MaxPerRound).Pluck("to_address", &addresses).Error
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, nil
	}
	var wallets []model.UserWallet
	err = store.UserWalletsByAddresses(ctx, addresses, &wallets)
	// err = store.MyStore.DB.WithContext(ctx).
	// 	Where("address IN ? AND purpose = ?", addresses, "deposit").Find(&wallets).Error
	return wallets, err
}

// minSweepUnits converts the runtime USDT threshold into token minimum units.
func (s *Service) minSweepUnits() *big.Int {
	usdt := s.pricer.MinSweepUSDT()
	scale := new(big.Float).SetFloat64(usdt)
	for i := 0; i < s.token.Decimals; i++ {
		scale.Mul(scale, big.NewFloat(10))
	}
	out, _ := scale.Int(nil)
	return out
}

// isStale allows dust to be swept eventually so it cannot pile up forever.
func (s *Service) isStale(ctx context.Context, address string) bool {
	days := config.Cfg.SweepServer.Threshold.StaleDays
	if days <= 0 {
		return false
	}
	var oldest model.DepositRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("to_address = ? AND status = ? AND swept = ?", address, model.DepositStateConfirmed, false).
		Order("id asc").Take(&oldest).Error
	if err != nil {
		return false
	}
	return time.Since(oldest.CreatedAt) > time.Duration(days)*24*time.Hour
}

func (s *Service) tokenBalance(ctx context.Context, address string) (*big.Int, error) {
	data, err := tron.EncodeTRC20BalanceOf(address)
	if err != nil {
		return nil, err
	}
	out, _, err := s.gw.TriggerConstantContract(ctx, address, s.token.Contract, data)
	if err != nil {
		return nil, err
	}
	value, ok := tron.ParseUint256(out)
	if !ok {
		return nil, fmt.Errorf("sweep: cannot parse balance %q", out)
	}
	return value, nil
}

// 1.查发起方 USDT 余额、查波场实时能量单价/租金
// 2.向第三方便宜平台租赁能量和宽带
// 3.构建未签名交易对象
// 4.本地私钥签名
// 5.广播交易
// 6.确认交易

// sweepOne performs the full sweep for a single address under a lock so two
// workers can never spend the same balance twice.
func (s *Service) sweepOne(ctx context.Context, wallet model.UserWallet, amount *big.Int) error {
	unlock, ok := store.MyStore.Lock(ctx, "sweep:"+wallet.Address, config.Duration(config.Cfg.SweepServer.LockTTL, 10*time.Minute))
	if !ok {
		return nil
	}
	defer unlock()

	// An in-flight sweep for this address means we must wait for its outcome.
	var inflight int64
	if err := store.MyStore.DB.WithContext(ctx).Model(&model.SweepRecord{}).
		Where("from_address = ? AND status IN ?", wallet.Address,
			[]string{model.SweepStateCreated, model.SweepStateEnergyOK, model.SweepStateBroadcast}).
		Count(&inflight).Error; err != nil {
		return err
	}
	if inflight > 0 {
		return nil
	}

	finance := config.Cfg.Wallet.FinanceWallet.Address
	data, err := tron.EncodeTRC20Transfer(finance, amount) //构建一个智能合约函数

	if err != nil {
		return err
	}
	record := &model.SweepRecord{
		FromAddress: wallet.Address,
		ToAddress:   finance,
		Symbol:      s.token.Symbol,
		Contract:    s.token.Contract,
		AmountUnits: amount.String(),
		Status:      model.SweepStateCreated,
	}
	if err := store.MyStore.DB.WithContext(ctx).Create(record).Error; err != nil {
		return err
	}

	// Build and sign first: a rental starts expiring the moment it is granted.
	//第3步： 请求节点组装一个未签名的智能合约字节码编译以及区块链的实时状态数据
	//交易对象(智能合约):组装一笔"触发已有 USDT 合约"的指令数据,并检查余额是否够
	tx, err := s.gw.BuildTRC20Transfer(ctx, wallet.Address, s.token.Contract, data, config.Cfg.SweepServer.FeeLimitSun)
	if err != nil {
		return s.failSweep(ctx, record, err)
	}
	//第4步：调用签名服务,用私钥签名
	signed, err := s.sign.Sign(ctx, &signer.SignRequest{
		Purpose: signer.PurposeSweep,
		Path:    wallet.DerivePath,
		Address: wallet.Address,
		Tx:      tx,
		Meta: signer.SignMeta{
			ToAddress:   finance,
			Contract:    s.token.Contract,
			AmountUnits: amount.String(),
		},
	})
	if err != nil {
		return s.failSweep(ctx, record, err)
	}
	store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
		"txid": signed.TxID, "signed_raw": signed.Tx.RawDataHex, "updated_at": time.Now(),
	})

	need, err := s.mgr.EstimateEnergy(ctx, wallet.Address, s.token.Contract, data)
	if err != nil {
		s.log.Warn("energy estimate failed, using configured worst case", "address", wallet.Address, ",err:", err)
		need = config.Cfg.Energy.EnergyPerTxNew
	}
	requestID := fmt.Sprintf("sweep-%d", record.ID)
	order, err := s.mgr.Acquire(ctx, "sweep", wallet.Address, need, requestID)
	if err != nil {
		return s.failSweep(ctx, record, fmt.Errorf("energy: %w", err))
	}
	store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
		"status":       model.SweepStateEnergyOK,
		"fee_mode":     energy.FeeMode(order.Provider),
		"energy_order": order.RequestID,
		"cost_trx":     order.CostTRX,
		"updated_at":   time.Now(),
	})
	//第5步骤 广播
	res, err := s.gw.Broadcast(ctx, signed.Tx)
	if err != nil {
		store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
			"status": model.SweepStateBroadcast, "fail_reason": truncate(err.Error(), 240), "updated_at": time.Now(),
		})
		return err
	}
	now := time.Now()
	store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
		"status": model.SweepStateBroadcast, "txid": res.TxID, "broadcast_at": now, "updated_at": now,
	})
	s.log.Info("sweep broadcast", "address", wallet.Address, "amount", amount.String(),
		"txid", res.TxID, "fee_mode", energy.FeeMode(order.Provider), "cost_trx", order.CostTRX)
	return nil
}

func (s *Service) failSweep(ctx context.Context, record *model.SweepRecord, cause error) error {
	store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
		"status": model.SweepStateFailed, "fail_reason": truncate(cause.Error(), 240), "updated_at": time.Now(),
	})
	return cause
}

// Reconcile confirms broadcast sweeps and marks the underlying deposits swept.
func (s *Service) Reconcile(ctx context.Context) error {
	var rows []model.SweepRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("status = ? AND txid <> ''", model.SweepStateBroadcast).
		Order("id asc").Limit(100).Find(&rows).Error
	if err != nil {
		return err
	}
	for i := range rows {
		row := rows[i]
		info, err := s.gw.GetTxInfoByID(ctx, row.TxID)
		if err != nil || info == nil {
			continue
		}
		now := time.Now()
		if !info.Succeeded() {
			store.MyStore.DB.WithContext(ctx).Model(&model.SweepRecord{}).Where("id = ?", row.ID).
				UpdateColumns(map[string]any{
					"status": model.SweepStateFailed, "fail_reason": info.Receipt.Result, "updated_at": now,
				})
			continue
		}
		err = store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.SweepRecord{}).
				Where("id = ? AND status = ?", row.ID, model.SweepStateBroadcast).
				UpdateColumns(map[string]any{
					"status":       model.SweepStateConfirmed,
					"confirmed_at": now,
					"energy_used":  info.Receipt.EnergyUsageTotal,
					"updated_at":   now,
				}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.DepositRecord{}).
				Where("to_address = ? AND status = ? AND swept = ?", row.FromAddress, model.DepositStateConfirmed, false).
				UpdateColumns(map[string]any{"swept": true, "updated_at": now}).Error; err != nil {
				return err
			}
			return tx.Where("address = ?", row.FromAddress).Delete(&model.SweepSkip{}).Error
		})
		if err != nil {
			s.log.Error("sweep reconcile failed", "id", row.ID, ",err:", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
