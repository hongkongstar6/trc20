// Package sweep moves USDT from user deposit addresses into the finance wallet.
//
// Each user address is its own account, so each sweep needs its own delegated
// energy. The order of the steps follows from the node writing an expiration
// (about a minute) into every transaction it assembles:
//
//  1. estimate the energy with a read-only constant call, which carries no
//     expiration and does not need the address to hold any energy at all;
//  2. rent that amount and wait for the delegation to be usable on chain;
//  3. only then assemble the real transaction, and sign and broadcast it
//     immediately, so the expiration window is never spent waiting for a
//     provider to deliver.
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
	"github.com/hongkongstar6/trc20/internal/transfer"
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
	// exec runs the transfer itself, which is the part a sweep shares with a
	// withdrawal: estimate, energy, build, sign, broadcast.
	exec *transfer.Executor
	log  *logrus.Logger
	// tokens are every enabled token. Each is swept on its own: one deposit
	// address can hold a USDT and a USDC balance at the same time and each of
	// them needs its own transfer, threshold and energy.
	tokens []config.TokenConfig
}

func New(gw *chain.Gateway, sign *signer.Client, mgr *energy.Manager, pricer *energy.Pricer) (*Service, error) {
	tokens := config.Cfg.EnabledTokens()
	if len(tokens) == 0 {
		return nil, errors.New("sweep: no enabled token configured")
	}
	if config.Cfg.Wallet.SweepWallet.Address == "" {
		return nil, errors.New("sweep: wallet.sweep_wallet.address is required")
	}
	return &Service{gw: gw, sign: sign, mgr: mgr, pricer: pricer,
		exec: transfer.NewExecutor(gw, sign, mgr), tokens: tokens, log: logrus.StandardLogger()}, nil
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

// round sweeps every enabled token, one candidate address at a time.
func (s *Service) round(ctx context.Context) error {
	var firstErr error
	for _, token := range s.tokens {
		if err := s.roundToken(ctx, token); err != nil {
			s.log.Error("sweep round failed", "symbol", token.Symbol, ",err:", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// roundToken picks the candidate addresses holding one token and sweeps them
// one by one.
func (s *Service) roundToken(ctx context.Context, token config.TokenConfig) error {
	candidates, err := s.candidates(ctx, token)
	if err != nil {
		return err
	}
	minUnits := s.minSweepUnits(token)
	maxSkips := config.Cfg.SweepServer.Threshold.MaxSkipRounds
	for _, addr := range candidates {
		balance, err := s.exec.TokenBalance(ctx, token.Contract, addr.Address)
		if err != nil {
			s.log.Error("balance query failed", "address", addr.Address,
				"symbol", token.Symbol, ",err:", err)
			continue
		}
		if balance.Sign() <= 0 {
			// Nothing left on chain: a previous sweep already moved the funds but
			// its deposits were not all marked, so the address would stay a
			// candidate forever. Close them out here.
			if err := s.markSweptEmpty(ctx, token, addr.Address); err != nil {
				s.log.Error("marking deposits of an emptied address failed", "address", addr.Address, ",err:", err)
			}
			continue
		}
		// Above the threshold the very first round already sweeps; below it the
		// address waits until it was skipped max_skip_rounds times (or went
		// stale), so small balances still reach the finance wallet eventually.
		if balance.Cmp(minUnits) < 0 {
			skips, err := s.recordSkip(ctx, token.Contract, addr.Address)
			if err != nil {
				s.log.Error("sweep skip counter failed", "address", addr.Address, ",err:", err)
				continue
			}
			forced := maxSkips > 0 && skips >= maxSkips
			if !forced && !s.isStale(ctx, token, addr.Address) {
				continue
			}
			s.log.Info("sweeping below-threshold address", "address", addr.Address, "symbol", token.Symbol,
				"balance", balance.String(), "min_units", minUnits.String(), "skips", skips)
		}
		if err := s.sweepOne(ctx, token, addr, balance); err != nil {
			s.log.Error("sweep failed", "address", addr.Address, "symbol", token.Symbol, ",err:", err)
		}
	}
	return nil
}

// recordSkip counts one more skipped round for the address and token and
// returns the new total. The counter is cleared once a sweep of that token from
// the address is confirmed.
func (s *Service) recordSkip(ctx context.Context, contract, address string) (int, error) {
	now := time.Now()
	err := store.MyStore.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "address"}, {Name: "contract"}},
		DoUpdates: clause.Assignments(map[string]any{
			"skip_count":   gorm.Expr("skip_count + 1"),
			"last_skip_at": now,
			"updated_at":   now,
		}),
	}).Create(&model.SweepSkip{Address: address, Contract: contract, SkipCount: 1, LastSkipAt: now}).Error
	if err != nil {
		return 0, err
	}
	var row model.SweepSkip
	if err := store.MyStore.DB.WithContext(ctx).
		Where("address = ? AND contract = ?", address, contract).Take(&row).Error; err != nil {
		return 0, err
	}
	return row.SkipCount, nil
}

// candidates are deposit addresses with confirmed unswept deposits of the token.
func (s *Service) candidates(ctx context.Context, token config.TokenConfig) ([]model.UserWallet, error) {
	var addresses []string
	err := store.MyStore.DB.WithContext(ctx).Model(&model.DepositRecord{}).
		Distinct("to_address").
		Where("status = ? AND swept = ? AND internal = ? AND contract = ?",
			model.DepositStateConfirmed, false, false, token.Contract).
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
func (s *Service) minSweepUnits(token config.TokenConfig) *big.Int {
	usdt := s.pricer.MinSweepUSDT()
	scale := new(big.Float).SetFloat64(usdt)
	for i := 0; i < token.Decimals; i++ {
		scale.Mul(scale, big.NewFloat(10))
	}
	out, _ := scale.Int(nil)
	return out
}

// isStale allows dust to be swept eventually so it cannot pile up forever.
func (s *Service) isStale(ctx context.Context, token config.TokenConfig, address string) bool {
	days := config.Cfg.SweepServer.Threshold.StaleDays
	if days <= 0 {
		return false
	}
	var oldest model.DepositRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("to_address = ? AND status = ? AND swept = ? AND contract = ?",
			address, model.DepositStateConfirmed, false, token.Contract).
		Order("id asc").Take(&oldest).Error
	if err != nil {
		return false
	}
	return time.Since(oldest.CreatedAt) > time.Duration(days)*24*time.Hour
}

// 1.查发起方 USDT 余额、查波场实时能量单价/租金
// 2.只读预执行估算本次转账需要的能量（不上链、不写 expiration）
// 3.按估算值向第三方平台租赁能量，并等委托到账
// 4.构建未签名交易对象（此时才开始计 expiration）
// 5.本地私钥签名
// 6.广播交易
// 7.确认交易

// sweepOne performs the full sweep of one token from a single address under a
// lock so two workers can never spend the same balance twice.
func (s *Service) sweepOne(ctx context.Context, token config.TokenConfig, wallet model.UserWallet, amount *big.Int) error {
	unlock, ok := store.MyStore.Lock(ctx, "sweep:"+token.Contract+":"+wallet.Address, config.Duration(config.Cfg.SweepServer.LockTTL, 10*time.Minute))
	if !ok {
		return nil
	}
	defer unlock()

	// An in-flight sweep of this token from this address means we must wait for
	// its outcome; a sweep of another token is unrelated and may run alongside.
	var inflight int64
	if err := store.MyStore.DB.WithContext(ctx).Model(&model.SweepRecord{}).
		Where("from_address = ? AND contract = ? AND status IN ?", wallet.Address, token.Contract,
			[]string{model.SweepStateCreated, model.SweepStateEnergyOK, model.SweepStateBroadcast}).
		Count(&inflight).Error; err != nil {
		return err
	}
	if inflight > 0 {
		return nil
	}

	// A retry after OUT_OF_ENERGY needs more head room than the first attempt,
	// and an address that keeps failing must stop burning fees.
	attempts, err := s.energyFailures(ctx, token.Contract, wallet.Address)
	if err != nil {
		return err
	}
	if budget := s.maxEnergyRetries(); attempts >= budget {
		s.log.Error("address exceeded the OUT_OF_ENERGY retry budget, manual handling required",
			"address", wallet.Address, "attempts", attempts, "max_retries", budget)
		return nil
	}

	finance := config.Cfg.Wallet.SweepWallet.Address
	data, err := tron.EncodeTRC20Transfer(finance, amount) //构建一个智能合约函数

	if err != nil {
		return err
	}
	// A retry after OUT_OF_ENERGY asks for more head room than the first attempt.
	factor := energy.RetrySafetyFactor(attempts)
	// record and order exist from the energy step on: the sweep row is only
	// written once the sweep is actually going to be attempted.
	var record *model.SweepRecord
	var order *model.EnergyRentOrder
	_, err = s.exec.Send(ctx, transfer.Request{
		Purpose:        signer.PurposeSweep,
		From:           wallet.Address,
		DerivePath:     wallet.DerivePath,
		To:             finance,
		Contract:       token.Contract,
		Amount:         amount,
		Data:           data,
		FeeLimitSun:    config.Cfg.SweepServer.FeeLimitSun,
		EnergyFactor:   factor,
		FallbackEnergy: int64(float64(config.Cfg.Energy.EnergyPerTxNew) * factor),
		ExpirationSec:  s.expirationSeconds(),
	}, transfer.Hooks{
		Energy: func(ctx context.Context, need int64) error {
			// With rental off the deposit address pays its own energy and bandwidth,
			// and a deposit address normally holds no TRX: without it the transfer
			// would revert with OUT_OF_ENERGY, so the address is left for the next
			// round and no sweep row is written for an attempt that never happened.
			if !s.mgr.RentalEnabled() {
				if err := s.checkBurnBudget(ctx, wallet.Address, need); err != nil {
					s.log.Warn("skipping sweep, the address cannot burn the fee", "address", wallet.Address, ",err:", err)
					return transfer.ErrStop
				}
			}
			// Only the deposits that exist now are covered by this sweep; one that
			// confirms while it is in flight keeps its unswept flag.
			depositMaxID, err := s.maxUnsweptDepositID(ctx, token.Contract, wallet.Address)
			if err != nil {
				return err
			}
			record = &model.SweepRecord{
				FromAddress:  wallet.Address,
				ToAddress:    finance,
				Symbol:       token.Symbol,
				Contract:     token.Contract,
				AmountUnits:  amount.String(),
				Status:       model.SweepStateCreated,
				RetryCount:   attempts,
				DepositMaxID: depositMaxID,
			}
			if err := store.MyStore.DB.WithContext(ctx).Create(record).Error; err != nil {
				return err
			}
			//按估算值租赁能量并等委托到账（关闭租赁时只登记为烧 TRX）
			acquired, err := s.mgr.Acquire(ctx, "sweep", wallet.Address, need, fmt.Sprintf("sweep-%d", record.ID))
			if err != nil {
				return s.failSweep(ctx, record, FailCodeEnergyRental, fmt.Errorf("energy: %w", err))
			}
			order = acquired
			store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
				"status":       model.SweepStateEnergyOK,
				"fee_mode":     energy.FeeMode(order.Provider),
				"energy_order": order.RequestID,
				"cost_trx":     order.CostTRX,
				"updated_at":   time.Now(),
			})
			return nil
		},
		Fail: func(ctx context.Context, failCode string, cause error) error {
			return s.failSweep(ctx, record, failCode, cause)
		},
		Signed: func(ctx context.Context, signed *transfer.Signed) error {
			store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(
				map[string]any{
					"txid":       signed.TxID,
					"signed_raw": signed.RawDataHex,
					"expired_at": signed.ExpiredAt,
					"updated_at": time.Now(),
				})
			return nil
		},
		Broadcast: func(ctx context.Context, out *transfer.Broadcast) error {
			now := time.Now()
			if out.Err != nil {
				// A node that rejected the transaction for a permanent reason will
				// never accept these bytes, so the row is failed now instead of being
				// rebroadcast every round until it expires.
				if out.Permanent {
					return s.failSweep(ctx, record, out.FailCode, out.Err)
				}
				store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
					"status": model.SweepStateBroadcast, "fail_reason": truncate(out.Err.Error(), 240),
					"fail_code": out.FailCode, "updated_at": now,
				})
				return nil
			}
			store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
				"status": model.SweepStateBroadcast, "txid": out.Result.TxID, "broadcast_at": now, "updated_at": now,
			})
			s.log.Info("sweep broadcast address: ", wallet.Address, "symbol: ", token.Symbol, "amount: ", amount.String(),
				"txid", out.Result.TxID, "fee_mode", energy.FeeMode(order.Provider), "cost_trx", order.CostTRX)
			return nil
		},
	})
	// The address is simply left for the next round: nothing was signed.
	if errors.Is(err, transfer.ErrStop) {
		return nil
	}
	return err
}

func (s *Service) failSweep(ctx context.Context, record *model.SweepRecord, failCode string, cause error) error {
	store.MyStore.DB.WithContext(ctx).Model(record).UpdateColumns(map[string]any{
		"status": model.SweepStateFailed, "fail_reason": truncate(cause.Error(), 240),
		"fail_code": failCode, "updated_at": time.Now(),
	})
	return cause
}

// FailCodeEnergyRental marks a sweep that never reached the chain because no
// provider could deliver the energy; nothing was signed or broadcast.
const FailCodeEnergyRental = "energy_rental"

// checkBurnBudget verifies the address holds enough TRX to pay the missing
// energy and bandwidth of this transfer itself, which is what
// energy.rental_enabled=false asks of it.
func (s *Service) checkBurnBudget(ctx context.Context, address string, need int64) error {
	budget, err := s.exec.BurnBudget(ctx, address, need)
	if err != nil {
		return err
	}
	if limit := config.Cfg.SweepServer.FeeLimitSun; limit > 0 && budget.CostSun > limit {
		return fmt.Errorf("burning TRX costs %d sun, above sweep_server.fee_limit_sun=%d", budget.CostSun, limit)
	}
	if !budget.Enough() {
		return fmt.Errorf("TRX insufficient to burn the fee: balance=%d sun required=%d sun (energy=%d)",
			budget.BalanceSun, budget.CostSun, need)
	}
	return nil
}

// energyFailures counts the consecutive OUT_OF_ENERGY failures of one token on
// an address since its last confirmed sweep, which is the retry attempt number.
func (s *Service) energyFailures(ctx context.Context, contract, address string) (int, error) {
	var lastConfirmed model.SweepRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("from_address = ? AND contract = ? AND status = ?", address, contract, model.SweepStateConfirmed).
		Order("id desc").Take(&lastConfirmed).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	var count int64
	err = store.MyStore.DB.WithContext(ctx).Model(&model.SweepRecord{}).
		Where("from_address = ? AND contract = ? AND status = ? AND fail_code = ? AND id > ?",
			address, contract, model.SweepStateFailed, chain.FailOutOfEnergy, lastConfirmed.ID).
		Count(&count).Error
	return int(count), err
}

func (s *Service) maxEnergyRetries() int {
	if config.Cfg.SweepServer.MaxEnergyRetries > 0 {
		return config.Cfg.SweepServer.MaxEnergyRetries
	}
	return 3
}

// maxUnsweptDepositID is the highest deposit id the current balance can come
// from. Deposits confirmed after this point are settled by a later sweep.
func (s *Service) maxUnsweptDepositID(ctx context.Context, contract, address string) (int64, error) {
	var maxID *int64
	err := store.MyStore.DB.WithContext(ctx).Model(&model.DepositRecord{}).
		Where("to_address = ? AND contract = ? AND status = ? AND swept = ?",
			address, contract, model.DepositStateConfirmed, false).
		Select("MAX(id)").Scan(&maxID).Error
	if err != nil || maxID == nil {
		return 0, err
	}
	return *maxID, nil
}

// markSweptEmpty closes out the deposits of an address whose on-chain balance is
// zero. It only applies once a sweep of that address confirmed, so a balance
// that never arrived is not written off.
func (s *Service) markSweptEmpty(ctx context.Context, token config.TokenConfig, address string) error {
	var swept int64
	if err := store.MyStore.DB.WithContext(ctx).Model(&model.SweepRecord{}).
		Where("from_address = ? AND contract = ? AND status = ?", address, token.Contract, model.SweepStateConfirmed).
		Count(&swept).Error; err != nil {
		return err
	}
	if swept == 0 {
		return nil
	}
	return store.MyStore.DB.WithContext(ctx).Model(&model.DepositRecord{}).
		Where("to_address = ? AND contract = ? AND status = ? AND swept = ?",
			address, token.Contract, model.DepositStateConfirmed, false).
		UpdateColumns(map[string]any{"swept": true, "updated_at": time.Now()}).Error
}

// Reconcile confirms broadcast sweeps and marks the underlying deposits swept.
// Rows that are signed but not on chain are handled too: while the signed bytes
// are still valid they are rebroadcast unchanged, and once they expired the row
// is failed so the balance is picked up by a later round instead of staying
// in-flight forever.
func (s *Service) Reconcile(ctx context.Context) error {
	var rows []model.SweepRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("status IN ? AND txid <> ''", []string{model.SweepStateEnergyOK, model.SweepStateBroadcast}).
		Order("id asc").Limit(100).Find(&rows).Error
	if err != nil {
		return err
	}
	for i := range rows {
		row := rows[i]
		info, err := s.gw.GetTxInfoByID(ctx, row.TxID)
		if err != nil {
			continue
		}
		now := time.Now()
		if info == nil {
			if err := s.settleAbsent(ctx, row, now); err != nil {
				s.log.Error("sweep reconcile absent tx failed", "id", row.ID, ",err:", err)
			}
			continue
		}
		if !info.Succeeded() {
			failCode := chain.ClassifyReceipt(info)
			reason := info.FailureReason()
			store.MyStore.DB.WithContext(ctx).Model(&model.SweepRecord{}).Where("id = ?", row.ID).
				UpdateColumns(map[string]any{
					"status": model.SweepStateFailed, "fail_reason": truncate(reason, 240),
					"fail_code": failCode, "updated_at": now,
				})
			// OUT_OF_ENERGY leaves the USDT untouched, so the next round retries the
			// address with more head room; anything else repeats identically and is
			// only worth an operator's attention.
			if failCode == chain.FailOutOfEnergy {
				s.log.Warn("sweep ran out of energy, retrying with more head room",
					"address", row.FromAddress, "txid", row.TxID, "attempt", row.RetryCount+1)
			} else {
				s.log.Error("sweep failed on chain", "address", row.FromAddress,
					"txid", row.TxID, "fail_code", failCode, "reason", reason)
			}
			continue
		}
		err = store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.SweepRecord{}).
				Where("id = ? AND status IN ?", row.ID,
					[]string{model.SweepStateEnergyOK, model.SweepStateBroadcast}).
				UpdateColumns(map[string]any{
					"status":       model.SweepStateConfirmed,
					"confirmed_at": now,
					"energy_used":  info.Receipt.EnergyUsageTotal,
					"updated_at":   now,
				}).Error; err != nil {
				return err
			}
			// Only the deposits this sweep actually covered are written off. A
			// deposit that confirmed while the sweep was in flight keeps its
			// unswept flag and is picked up by the next round.
			deposits := tx.Model(&model.DepositRecord{}).
				Where("to_address = ? AND contract = ? AND status = ? AND swept = ?",
					row.FromAddress, row.Contract, model.DepositStateConfirmed, false)
			if row.DepositMaxID > 0 {
				deposits = deposits.Where("id <= ?", row.DepositMaxID)
			}
			if err := deposits.UpdateColumns(map[string]any{"swept": true, "updated_at": now}).Error; err != nil {
				return err
			}
			return tx.Where("address = ? AND contract = ?", row.FromAddress, row.Contract).
				Delete(&model.SweepSkip{}).Error
		})
		if err != nil {
			s.log.Error("sweep reconcile failed", "id", row.ID, ",err:", err)
		}
	}
	return nil
}

// settleAbsent handles a signed sweep that is not on chain. Rebroadcasting the
// stored bytes is the only safe retry: building a second transaction while the
// first one may still be included would sweep the same balance twice.
func (s *Service) settleAbsent(ctx context.Context, row model.SweepRecord, now time.Time) error {
	if row.ExpiredAt == nil || now.Before(*row.ExpiredAt) {
		if row.SignedRaw == "" {
			return nil
		}
		return s.rebroadcast(ctx, row)
	}
	// Expired without inclusion: it can never make it on chain now, so the row
	// is released and the next round sweeps the balance with a fresh rental.
	return store.MyStore.DB.WithContext(ctx).Model(&model.SweepRecord{}).
		Where("id = ? AND status IN ?", row.ID,
			[]string{model.SweepStateEnergyOK, model.SweepStateBroadcast}).
		UpdateColumns(map[string]any{
			"status":      model.SweepStateFailed,
			"fail_reason": "transaction expired without inclusion",
			"fail_code":   chain.FailExpired,
			"updated_at":  now,
		}).Error
}

// rebroadcast sends the stored bytes of a signed sweep again.
func (s *Service) rebroadcast(ctx context.Context, row model.SweepRecord) error {
	var wallet model.UserWallet
	if err := store.MyStore.DB.WithContext(ctx).
		Where("address = ?", row.FromAddress).Take(&wallet).Error; err != nil {
		return err
	}
	res, err := s.exec.Rebroadcast(ctx, transfer.RebroadcastRequest{
		Purpose:     signer.PurposeSweep,
		From:        row.FromAddress,
		DerivePath:  wallet.DerivePath,
		To:          row.ToAddress,
		Contract:    row.Contract,
		AmountUnits: row.AmountUnits,
		TxID:        row.TxID,
		SignedRaw:   row.SignedRaw,
	})
	if err == nil {
		return nil
	}
	// Retrying a permanent rejection every round until the transaction expires
	// only delays the fresh attempt the balance needs.
	if res != nil {
		if failCode, permanent := chain.ClassifyBroadcast(res.Code, res.Message); permanent {
			return s.failSweep(ctx, &row, failCode, err)
		}
	}
	return err
}

// expirationSeconds must mirror the node's transaction expiration, which is how
// long the signed bytes stay broadcastable.
func (s *Service) expirationSeconds() int64 {
	if config.Cfg.SweepServer.TxExpirationSec > 0 {
		return config.Cfg.SweepServer.TxExpirationSec
	}
	return 60
}

func truncate(s string, n int) string { return transfer.Truncate(s, n) }
