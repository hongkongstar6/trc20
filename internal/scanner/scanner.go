// Package scanner turns TRC20 Transfer logs into deposit records.
//
// Deposits are never inferred from balance changes: only a successful
// transaction emitting a Transfer log from an allowlisted contract to a known
// address counts. Records are notified to the business system only after the
// configured confirmation depth, and a reorg rolls them back to "orphaned".
package scanner

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/hongkongstar6/trc20/internal/tron"
	"github.com/sirupsen/logrus"
)

const cursorName = "tron_deposit"

type token struct {
	symbol   string
	decimals int
}

type Scanner struct {
	//cfg *config.Config
	//st *store.Store
	gw *chain.Gateway
	//log     *logrus.Logger
	tokens  map[string]token // contract (base58) -> token
	minUnit *big.Int
}

func New(gw *chain.Gateway) *Scanner {
	tokens := map[string]token{}
	for _, t := range config.Cfg.Wallet.Tokens {
		if t.Enabled {
			tokens[t.Contract] = token{symbol: t.Symbol, decimals: t.Decimals}
		}
	}
	minUnit := new(big.Int)
	if _, ok := minUnit.SetString(config.Cfg.Deposit.MinDepositUnits, 10); !ok {
		minUnit = big.NewInt(0)
	}
	return &Scanner{
		gw:      gw,
		tokens:  tokens,
		minUnit: minUnit,
		//st: st,
		//log:    log,
	}
}

// Run scans forward continuously until ctx is cancelled.
func (s *Scanner) Run(ctx context.Context) error {
	interval := config.Duration(config.Cfg.Deposit.PollInterval, 3*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.tick(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// A node outage must stall the cursor, never skip blocks.
			logrus.Error("scan tick failed", ",err:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Scanner) tick(ctx context.Context) error {
	head, err := s.gw.GetNowBlock(ctx)
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	cursor, err := loadCursor(ctx, head.Number())
	if err != nil {
		return err
	}
	if err := s.confirmUpTo(ctx, head.Number()); err != nil {
		logrus.Error("confirm pass failed", ",err:", err)
	}

	from := cursor.BlockNumber + 1
	to := min64(from+config.Cfg.Deposit.BatchBlocks-1, head.Number())
	if from > to {
		return nil
	}
	for num := from; num <= to; num++ {
		block, err := s.gw.GetBlockByNum(ctx, num)
		if err != nil {
			return fmt.Errorf("block %d: %w", num, err)
		}
		if cursor.BlockHash != "" && block.BlockHeader.RawData.ParentHash != "" &&
			!strings.EqualFold(block.BlockHeader.RawData.ParentHash, cursor.BlockHash) {
			logrus.Warn("reorg detected", "block", num, "parent", block.BlockHeader.RawData.ParentHash, "cursor_hash", cursor.BlockHash)
			newCursor, err := s.handleReorg(ctx, num)
			if err != nil {
				return fmt.Errorf("reorg: %w", err)
			}
			cursor = newCursor
			return nil // restart from the rolled back cursor on the next tick
		}
		//扫描区块数据，提取出转账事件，写入数据库
		if err := s.scanBlock(ctx, block); err != nil {
			return fmt.Errorf("scan block %d: %w", num, err)
		}
		cursor.BlockNumber = block.Number()
		cursor.BlockHash = block.BlockID
		if err := saveCursor(ctx, cursor); err != nil {
			return err
		}
	}
	return s.pruneSnapshots(ctx, to)
}

// 从数据库加载游标，如果没有则创建一个新的游标
func loadCursor(ctx context.Context, head int64) (*model.ChainCursor, error) {
	var c model.ChainCursor
	err := store.MyStore.DB.WithContext(ctx).Where("name = ?", cursorName).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		start := config.Cfg.Deposit.StartBlock
		if start <= 0 {
			start = head - 1
		}
		c = model.ChainCursor{Name: cursorName, BlockNumber: start}
		if err := store.MyStore.DB.WithContext(ctx).Create(&c).Error; err != nil {
			return nil, err
		}
		return &c, nil
	}
	return &c, err
}

// 保存数据库游标
func saveCursor(ctx context.Context, c *model.ChainCursor) error {
	return store.MyStore.DB.WithContext(ctx).Model(&model.ChainCursor{}).
		Where("name = ?", cursorName).
		UpdateColumns(map[string]any{
			"block_number": c.BlockNumber,
			"block_hash":   c.BlockHash,
			"updated_at":   time.Now(),
		}).Error
}

// scanBlock parses every Transfer log of one block in a single transaction.
func (s *Scanner) scanBlock(ctx context.Context, block *chain.Block) error {
	infos, err := s.gw.GetTxInfoByBlockNum(ctx, block.Number())
	if err != nil {
		return err
	}
	blockTime := time.UnixMilli(block.Timestamp())
	records := make([]model.DepositRecord, 0, 8)
	for i := range infos {
		info := &infos[i]
		if !info.Succeeded() {
			continue // reverted transactions must never create a deposit
		}
		for idx, lg := range info.Log {
			rec, ok, err := s.parseLog(ctx, info, lg, idx, block, blockTime)
			if err != nil {
				return err
			}
			if ok {
				records = append(records, *rec)
			}
		}
	}
	return store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot := model.BlockSnapshot{
			BlockNumber: block.Number(),
			BlockHash:   block.BlockID,
			ParentHash:  block.BlockHeader.RawData.ParentHash,
			BlockTime:   blockTime,
		}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&snapshot).Error; err != nil {
			return err
		}
		for i := range records {
			// (txid, event_index) is unique: replaying a block is a no-op.
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&records[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// transfer is a decoded TRC20 Transfer log from an allowlisted contract.
type transfer struct {
	contract string
	token    token
	from     string
	to       string
	amount   *big.Int
}

// decodeTransfer applies every chain level check that does not need the
// database. A log has to be an indexed 3 topic Transfer event of an
// allowlisted contract carrying a positive amount above the dust threshold;
// anything else is not a deposit.
// 解析区块数据，提取出转账事件，返回转账信息
func (s *Scanner) decodeTransfer(lg chain.TxLog) (*transfer, bool) {
	if len(lg.Topics) != 3 || !strings.EqualFold(lg.Topics[0], tron.TransferEventTopic) {
		return nil, false
	}
	contract, err := tron.HexToAddress(lg.Address)
	if err != nil {
		return nil, false
	}
	tk, ok := s.tokens[contract]
	if !ok {
		return nil, false // contract allowlist
	}
	fromAddr, err := tron.HexToAddress(lg.Topics[1])
	if err != nil {
		return nil, false
	}
	toAddr, err := tron.HexToAddress(lg.Topics[2])
	if err != nil {
		return nil, false
	}
	amount, ok := tron.ParseUint256(lg.Data)
	if !ok || amount.Sign() <= 0 {
		return nil, false
	}
	if s.minUnit.Sign() > 0 && amount.Cmp(s.minUnit) < 0 {
		return nil, false // dust filter
	}
	return &transfer{
		contract: contract,
		token:    tk,
		from:     fromAddr,
		to:       toAddr,
		amount:   amount}, true
}

// parseLog validates one log entry and resolves the owning user.
func (s *Scanner) parseLog(ctx context.Context, info *chain.TxInfo, lg chain.TxLog, idx int, block *chain.Block, blockTime time.Time) (*model.DepositRecord, bool, error) {
	t, ok := s.decodeTransfer(lg)
	if !ok {
		return nil, false, nil
	}
	var wallet model.UserWallet
	err := store.MyStore.DB.WithContext(ctx).Where("address = ?", t.to).Take(&wallet).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	// Internal movement (sweep, hot wallet refill) is recorded but flagged so
	// the business system never credits it as a user deposit.
	internal, err := s.isInternal(ctx, t.from)
	if err != nil {
		return nil, false, err
	}
	return &model.DepositRecord{
		MerchantID:  wallet.MerchantID,
		UID:         wallet.UID,
		Chain:       "TRON",
		Symbol:      t.token.symbol,
		Contract:    t.contract,
		TxID:        info.ID,
		EventIndex:  idx,
		BlockNumber: block.Number(),
		BlockHash:   block.BlockID,
		FromAddress: t.from,
		ToAddress:   t.to,
		AmountUnits: t.amount.String(),
		Decimals:    t.token.decimals,
		Status:      model.DepositStatePending,
		Internal:    internal || wallet.Purpose != "deposit",
		BlockTime:   blockTime,
	}, true, nil
}

func (s *Scanner) isInternal(ctx context.Context, from string) (bool, error) {
	var count int64
	if err := store.MyStore.DB.WithContext(ctx).Model(&model.UserWallet{}).Where("address = ?", from).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// confirmUpTo promotes pending records that reached the confirmation depth and
// enqueues exactly one outbox event per record.
func (s *Scanner) confirmUpTo(ctx context.Context, head int64) error {
	limit := head - config.Cfg.Deposit.Confirmations
	if limit <= 0 {
		return nil
	}
	var pending []model.DepositRecord
	err := store.MyStore.DB.WithContext(ctx).
		Where("status = ? AND block_number <= ?", model.DepositStatePending, limit).
		Order("id asc").Limit(500).Find(&pending).Error
	if err != nil {
		return err
	}
	for i := range pending {
		rec := pending[i]
		if err := s.confirmOne(ctx, rec, head); err != nil {
			logrus.Error("confirm deposit failed", "txid", rec.TxID, "event_index", rec.EventIndex, ",err:", err)
		}
	}
	return nil
}

func (s *Scanner) confirmOne(ctx context.Context, rec model.DepositRecord, head int64) error {
	// Re-read the receipt: if the block was replaced, the transaction may be
	// gone or may now belong to a different block.
	info, err := s.gw.GetTxInfoByID(ctx, rec.TxID)
	if err != nil {
		return err
	}
	now := time.Now()
	if info == nil || !info.Succeeded() {
		return store.MyStore.DB.WithContext(ctx).Model(&model.DepositRecord{}).
			Where("id = ? AND status = ?", rec.ID, model.DepositStatePending).
			UpdateColumns(map[string]any{"status": model.DepositStateOrphaned, "updated_at": now}).Error
	}
	if info.BlockNumber != rec.BlockNumber {
		logrus.Warn("deposit moved to another block", "txid", rec.TxID, "old", rec.BlockNumber, "new", info.BlockNumber)
		rec.BlockNumber = info.BlockNumber
	}
	return store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.DepositRecord{}).
			Where("id = ? AND status = ?", rec.ID, model.DepositStatePending).
			UpdateColumns(map[string]any{
				"status":        model.DepositStateConfirmed,
				"block_number":  rec.BlockNumber,
				"confirmations": head - rec.BlockNumber,
				"confirmed_at":  now,
				"updated_at":    now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // another worker already confirmed it
		}
		if rec.Internal {
			return nil // internal movements are not user deposits
		}
		event := map[string]any{
			"event_id":     depositEventID(rec),
			"type":         "deposit",
			"merchant_id":  rec.MerchantID,
			"uid":          rec.UID,
			"chain":        rec.Chain,
			"symbol":       rec.Symbol,
			"contract":     rec.Contract,
			"address":      rec.ToAddress,
			"from_address": rec.FromAddress,
			"amount":       rec.AmountUnits,
			"decimals":     rec.Decimals,
			"txid":         rec.TxID,
			"event_index":  rec.EventIndex,
			"block_number": rec.BlockNumber,
			"confirmed_at": now.Unix(),
		}
		return store.EnqueueOutbox(tx, depositEventID(rec), "deposit", rec.MerchantID, event)
	})
}

func depositEventID(rec model.DepositRecord) string {
	return fmt.Sprintf("%s:%d", rec.TxID, rec.EventIndex)
}

// handleReorg walks back to the last block whose stored hash still matches the
// chain, orphans the unconfirmed records above it and rewinds the cursor.
func (s *Scanner) handleReorg(ctx context.Context, detectedAt int64) (*model.ChainCursor, error) {
	forkPoint := detectedAt - 1
	limit := detectedAt - config.Cfg.Deposit.ReorgDepth
	for ; forkPoint > 0 && forkPoint >= limit; forkPoint-- {
		var snap model.BlockSnapshot
		err := store.MyStore.DB.WithContext(ctx).Where("block_number = ?", forkPoint).Take(&snap).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		onChain, err := s.gw.GetBlockByNum(ctx, forkPoint)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(onChain.BlockID, snap.BlockHash) {
			break
		}
	}
	if forkPoint <= 0 || forkPoint < limit {
		return nil, fmt.Errorf("reorg deeper than reorg_depth=%d, manual intervention required", config.Cfg.Deposit.ReorgDepth)
	}
	cursor := &model.ChainCursor{Name: cursorName, BlockNumber: forkPoint}
	err := store.MyStore.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.DepositRecord{}).
			Where("block_number > ? AND status = ?", forkPoint, model.DepositStatePending).
			UpdateColumns(map[string]any{"status": model.DepositStateOrphaned, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		if err := tx.Where("block_number > ?", forkPoint).Delete(&model.BlockSnapshot{}).Error; err != nil {
			return err
		}
		var snap model.BlockSnapshot
		if err := tx.Where("block_number = ?", forkPoint).Take(&snap).Error; err == nil {
			cursor.BlockHash = snap.BlockHash
		}
		return tx.Model(&model.ChainCursor{}).Where("name = ?", cursorName).
			UpdateColumns(map[string]any{
				"block_number": cursor.BlockNumber,
				"block_hash":   cursor.BlockHash,
				"updated_at":   time.Now(),
			}).Error
	})
	return cursor, err
}

func (s *Scanner) pruneSnapshots(ctx context.Context, head int64) error {
	keepFrom := head - config.Cfg.Deposit.ReorgDepth*10
	if keepFrom <= 0 {
		return nil
	}
	return store.MyStore.DB.WithContext(ctx).Where("block_number < ?", keepFrom).Delete(&model.BlockSnapshot{}).Error
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
