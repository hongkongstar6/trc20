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
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hongkongstar6/trc20/internal/bloom"
	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/hongkongstar6/trc20/internal/tron"
	"github.com/sirupsen/logrus"
)

// legacyCursorName is the network agnostic name used before the cursor was
// namespaced per network. A cursor written while pointing at nile sits 15M
// blocks away from the mainnet head, so reusing it stalls the scanner in
// ancient history instead of following the chain.
const legacyCursorName = "tron_deposit"

// cursorName isolates the scan position per network.
func cursorName() string {
	net := strings.ToLower(strings.TrimSpace(config.Cfg.Network))
	if net == "" {
		net = "unknown"
	}
	return legacyCursorName + ":" + net
}

type token struct {
	symbol   string
	decimals int
}

type Scanner struct {
	gw      *chain.Gateway
	tokens  map[string]token // contract (base58) -> token
	minUnit *big.Int
	// the cursor is validated against the chain once per process start
	cursorChecked bool
	//cfg *config.Config
	//st *store.Store
	//log     *logrus.Logger
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
	// The allowlist decides which logs can be a deposit at all, so an empty or
	// unexpected one has to be visible in the log of every scanner start.
	contracts := make([]string, 0, len(tokens))
	for c, t := range tokens {
		contracts = append(contracts, t.symbol+"="+c)
	}
	logrus.Info("token allowlist loaded,contracts:", strings.Join(contracts, ","),
		",min_deposit_units:", minUnit.String())
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
	// Addresses allocated by the api process are picked up by this poll, the
	// api only extends its own copy of the filter.
	go bloom.AddrFilter.RunSync(ctx)
	interval := config.Duration(config.Cfg.Deposit.PollInterval, 3*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		behind, err := s.tick(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// A node outage must stall the cursor, never skip blocks.
			logrus.Error("scan tick failed,err:", err)
		}
		if err == nil && behind {
			// Still lagging: keep scanning instead of idling for a whole
			// interval, otherwise the backlog only shrinks by one batch per
			// tick while the chain keeps producing blocks.
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// tick scans one batch. It reports whether the cursor is still behind the head
// after the batch, so Run can skip the poll interval while catching up.
func (s *Scanner) tick(ctx context.Context) (bool, error) {
	head, err := s.gw.GetNowBlock(ctx)
	if err != nil {
		return false, fmt.Errorf("head: %w", err)
	}
	cursor, err := s.loadCursor(ctx, head.Number())
	if err != nil {
		return false, err
	}
	if err := s.confirmUpTo(ctx, head.Number()); err != nil {
		logrus.Error("confirm pass failed", ",err:", err)
	}

	from := cursor.BlockNumber + 1
	to := min64(from+config.Cfg.Deposit.BatchBlocks-1, head.Number())
	if from > to {
		return false, nil
	}
	// A block costs two sequential RPC round trips, so a strictly serial
	// scanner barely keeps up with the block rate and never recovers a
	// backlog. Blocks are prefetched concurrently and still applied in order.
	fetched := s.fetchRange(ctx, from, to)
	for i := range fetched {
		num := from + int64(i)
		if fetched[i].err != nil {
			return false, fmt.Errorf("block %d: %w", num, fetched[i].err)
		}
		logrus.Debug("当前区块读取成功：", num)

		block := fetched[i].block
		if cursor.BlockHash != "" && block.BlockHeader.RawData.ParentHash != "" &&
			!strings.EqualFold(block.BlockHeader.RawData.ParentHash, cursor.BlockHash) {
			logrus.Warn("reorg detected,block:", num, ",parent:", block.BlockHeader.RawData.ParentHash, ",cursor_hash:", cursor.BlockHash)
			newCursor, err := s.handleReorg(ctx, num)
			if err != nil {
				return false, fmt.Errorf("reorg: %w", err)
			}
			cursor = newCursor
			return true, nil // restart from the rolled back cursor
		}
		//扫描区块数据，提取出转账事件，写入数据库
		if err := s.scanBlock(ctx, block, fetched[i].infos); err != nil {
			return false, fmt.Errorf("scan block %d: %w", num, err)
		}
		cursor.BlockNumber = block.Number()
		cursor.BlockHash = block.BlockID
		if err := saveCursor(ctx, cursor); err != nil {
			return false, err
		}
	}
	return to < head.Number(), s.pruneSnapshots(ctx, to)
}

// blockData is one prefetched block together with its receipts.
type blockData struct {
	block *chain.Block
	infos []chain.TxInfo
	err   error
}

// fetchRange downloads [from, to] concurrently and returns the results in
// ascending block order, so reorg detection and cursor updates stay sequential.
func (s *Scanner) fetchRange(ctx context.Context, from, to int64) []blockData {
	out := make([]blockData, to-from+1)
	workers := int(config.Cfg.Deposit.FetchConcurrency)
	if workers <= 0 {
		workers = 1
	}
	if workers > len(out) {
		workers = len(out)
	}
	nums := make(chan int64, len(out))
	for num := from; num <= to; num++ {
		nums <- num
	}
	close(nums)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for num := range nums {
				block, err := s.gw.GetBlockByNum(ctx, num)
				if err != nil {
					out[num-from] = blockData{err: err}
					continue
				}
				infos, err := s.gw.GetTxInfoByBlockNum(ctx, num)
				out[num-from] = blockData{block: block, infos: infos, err: err}
			}
		}()
	}
	wg.Wait()
	return out
}

// loadCursor returns the scan position, creating it on the first run and
// realigning it when it cannot belong to the chain the gateway is talking to
// (a cursor left behind by another network, or a corrupted height).
func (s *Scanner) loadCursor(ctx context.Context, head int64) (*model.ChainCursor, error) {
	name := cursorName()
	var c model.ChainCursor
	err := store.MyStore.DB.WithContext(ctx).Where("name = ?", name).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		start := config.Cfg.Deposit.StartBlock
		if start <= 0 || start > head {
			start = head - 1
		}
		// Adopt the pre-namespacing cursor, but only when it plausibly comes
		// from this chain: otherwise the nile height would be inherited again.
		var legacy model.ChainCursor
		if store.MyStore.DB.WithContext(ctx).Where("name = ?", legacyCursorName).Take(&legacy).Error == nil {
			if ok, err := s.cursorOnChain(ctx, &legacy, head); err != nil {
				return nil, err
			} else if ok {
				start = legacy.BlockNumber
				c.BlockHash = legacy.BlockHash
			} else {
				logrus.Warn("legacy cursor does not belong to this chain, starting from head",
					"network", config.Cfg.Network, "legacy_block", legacy.BlockNumber, "head", head)
			}
		}
		c.Name, c.BlockNumber = name, start
		if err := store.MyStore.DB.WithContext(ctx).Create(&c).Error; err != nil {
			return nil, err
		}
		return &c, nil
	}
	if err != nil {
		return nil, err
	}
	if s.cursorChecked {
		return &c, nil
	}
	ok, err := s.cursorOnChain(ctx, &c, head)
	if err != nil {
		return nil, err
	}
	s.cursorChecked = true
	if !ok {
		logrus.Error("cursor does not belong to this chain, realigning to head",
			"network", config.Cfg.Network, "cursor_block", c.BlockNumber, "head", head)
		c.BlockNumber, c.BlockHash = head-1, ""
		if err := saveCursor(ctx, &c); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// cursorOnChain reports whether the stored position can be a position of the
// chain the gateway serves. A height above the head is impossible, and a
// stored hash that no longer matches a block far below the head is a foreign
// chain rather than a reorg (those stay within reorg_depth of the head).
func (s *Scanner) cursorOnChain(ctx context.Context, c *model.ChainCursor, head int64) (bool, error) {
	if c.BlockNumber > head {
		return false, nil
	}
	if c.BlockHash == "" || head-c.BlockNumber <= config.Cfg.Deposit.ReorgDepth {
		return true, nil
	}
	block, err := s.gw.GetBlockByNum(ctx, c.BlockNumber)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(block.BlockID, c.BlockHash), nil
}

// 保存数据库游标
func saveCursor(ctx context.Context, c *model.ChainCursor) error {
	logrus.Debug("更新游标区块：", c.BlockNumber)
	return store.MyStore.DB.WithContext(ctx).Model(&model.ChainCursor{}).
		Where("name = ?", c.Name).
		UpdateColumns(map[string]any{
			"block_number": c.BlockNumber,
			"block_hash":   c.BlockHash,
			"updated_at":   time.Now(),
		}).Error
}

// scanBlock parses every Transfer log of one block in a single transaction.
func (s *Scanner) scanBlock(ctx context.Context, block *chain.Block, infos []chain.TxInfo) error {
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
		//只关心 USDT 的 Transfer 事件，其 address 都是固定的 USDT 合约地址(TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t)
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
	// The bloom filter answers "definitely not ours" for virtually every
	// recipient on chain, so only the few possible hits reach MySQL.
	if !bloom.AddrFilter.MayContain(t.to) {
		logrus.Debug("地址不属于本系统：", t.to)
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
	logrus.Info("地址匹配成功,address:", t.to, ",txid:", info.ID, ",amount:", t.amount.String())
	// Internal movement (sweep, hot wallet refill) is recorded but flagged so
	// the business system never credits it as a user deposit.
	internal, err := s.isInternal(ctx, t.from)
	if err != nil {
		return nil, false, err
	}
	return &model.DepositRecord{
		MerchantID:  wallet.MerchantID,
		Account:     wallet.Account,
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
	if !bloom.AddrFilter.MayContain(from) {
		return false, nil
	}
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
			"account":      rec.Account,
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
	cursor := &model.ChainCursor{Name: cursorName(), BlockNumber: forkPoint}
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
		logrus.Info("更新区块1：", cursor.BlockNumber)
		return tx.Model(&model.ChainCursor{}).Where("name = ?", cursor.Name).
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
