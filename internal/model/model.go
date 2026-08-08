package model

import "time"

// Deposit / withdraw / sweep states. States only move forward; every worker
// uses a compare-and-swap update so a duplicated run cannot double process.
const (
	DepositStatePending   = "pending"
	DepositStateConfirmed = "confirmed"
	DepositStateOrphaned  = "orphaned"

	WithdrawStateCreated   = "created"
	WithdrawStateSigned    = "signed"
	WithdrawStateBroadcast = "broadcast"
	WithdrawStateConfirmed = "confirmed"
	WithdrawStateFailed    = "failed"
	WithdrawStateRejected  = "rejected"

	SweepStateCreated   = "created"
	SweepStateEnergyOK  = "energy_ready"
	SweepStateBroadcast = "broadcast"
	SweepStateConfirmed = "confirmed"
	SweepStateFailed    = "failed"

	OutboxStatePending = "pending"
	OutboxStateSent    = "sent"
	OutboxStateDead    = "dead"

	EnergyOrderCreated   = "created"
	EnergyOrderDelegated = "delegated"
	EnergyOrderFailed    = "failed"

	TopupStateCreated   = "created"
	TopupStateBroadcast = "broadcast"
	TopupStateConfirmed = "confirmed"
	TopupStateCredited  = "credited"
	TopupStateFailed    = "failed"

	MerchantStatusOff int8 = 0
	MerchantStatusOn  int8 = 1
)

// Merchant is the tenant every user belongs to. Its secret signs both the
// inbound API parameters and the outbound deposit callbacks, so it never leaves
// the wallet system and is not serialised in API responses.
type Merchant struct {
	ID          int64     `gorm:"primaryKey" json:"id"`
	MerchantID  string    `gorm:"column:merchant_id;size:64;uniqueIndex" json:"merchant_id"`
	Name        string    `gorm:"size:64" json:"name"`
	CallbackURL string    `gorm:"size:255" json:"callback_url"`
	Secret      string    `gorm:"size:128" json:"-"` // sha256 signing key
	Status      int8      `json:"status"`            // 1 enabled | 0 disabled
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Merchant) TableName() string { return "merchant" }

// Wallet is a derived address. Private keys never live here: only the
// derivation path, which is meaningless without the seed held by sign-service.
type Wallet struct {
	ID         int64  `gorm:"primaryKey" json:"id"`
	MerchantID string `gorm:"column:merchant_id;size:64;index" json:"merchant_id"`
	UID        string `gorm:"column:uid;index" json:"uid"`
	// Account is merchant_id + "_" + uid, the unique account a deposit address
	// is allocated for: the same uid under two merchants is two accounts.
	// Platform owned wallets (hot, finance, gas) leave it NULL.
	Account    string    `gorm:"size:128;uniqueIndex" json:"account"`
	Chain      string    `gorm:"size:16" json:"chain"`
	Address    string    `gorm:"size:64;uniqueIndex" json:"address"`
	AddrIndex  int64     `gorm:"column:addr_index;uniqueIndex:uq_chain_index,priority:2" json:"addr_index"`
	ChainIdx   string    `gorm:"column:chain_idx;size:16;uniqueIndex:uq_chain_index,priority:1" json:"-"`
	DerivePath string    `gorm:"size:64" json:"derive_path"`
	Purpose    string    `gorm:"size:16;index" json:"purpose"` // deposit | hot | finance | gas
	Status     int8      `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (Wallet) TableName() string { return "wallet" }

// WalletIndexAllocator hands out monotonic derivation indexes. The uid is
// never used as an index: it would leak business scale and overflow the path.
type WalletIndexAllocator struct {
	Chain     string    `gorm:"size:16;primaryKey" json:"chain"`
	NextIndex int64     `json:"next_index"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (WalletIndexAllocator) TableName() string { return "wallet_index_allocator" }

// DepositRecord is one TRC20 Transfer log targeting one of our addresses.
// Uniqueness is (txid, event_index), which is also the downstream event id.
//光靠游标还不够，因为进程可能在扫到块、但还没保存游标之前就崩溃/重启，导致同一个块被重扫。为此，DepositRecord 表上以 (txid, event_index) 建了唯一索引 uq_tx_event
type DepositRecord struct {
	ID            int64      `gorm:"primaryKey" json:"id"`
	MerchantID    string     `gorm:"column:merchant_id;size:64;index" json:"merchant_id"`
	UID           string     `gorm:"column:uid;index" json:"uid"`
	Chain         string     `gorm:"size:16" json:"chain"`
	Symbol        string     `gorm:"size:16" json:"symbol"`
	Contract      string     `gorm:"size:64" json:"contract"`
	TxID          string     `gorm:"column:txid;size:70;uniqueIndex:uq_tx_event,priority:1" json:"txid"`
	EventIndex    int        `gorm:"size:32;uniqueIndex:uq_tx_event,priority:2" json:"event_index"`
	BlockNumber   int64      `gorm:"index" json:"block_number"`
	BlockHash     string     `gorm:"size:70" json:"block_hash"`
	FromAddress   string     `gorm:"size:64" json:"from_address"`
	ToAddress     string     `gorm:"size:64;index" json:"to_address"`
	AmountUnits   string     `gorm:"type:decimal(38,0)" json:"amount_units"`
	Decimals      int        `gorm:"size:32" json:"decimals"`
	Confirmations int64      `json:"confirmations"`
	Status        string     `gorm:"size:16;index" json:"status"`
	Internal      bool       `json:"internal"`
	Swept         bool       `gorm:"index" json:"swept"`
	BlockTime     time.Time  `json:"block_time"`
	ConfirmedAt   *time.Time `json:"confirmed_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (DepositRecord) TableName() string { return "deposit_record" }

// WithdrawRecord holds one business withdrawal order. biz_order_no is unique,
// which is what guarantees "at most one on-chain transfer per business order".
type WithdrawRecord struct {
	ID          int64  `gorm:"primaryKey" json:"id"`
	BizOrderNo  string `gorm:"size:64;uniqueIndex" json:"biz_order_no"`
	UID         int64  `gorm:"column:uid;index" json:"uid"`
	Chain       string `gorm:"size:16" json:"chain"`
	Symbol      string `gorm:"size:16" json:"symbol"`
	Contract    string `gorm:"size:64" json:"contract"`
	FromAddress string `gorm:"size:64" json:"from_address"`
	ToAddress   string `gorm:"size:64;index" json:"to_address"`
	AmountUnits string `gorm:"type:decimal(38,0)" json:"amount_units"`
	Decimals    int    `gorm:"size:32" json:"decimals"`
	Status      string `gorm:"size:16;index" json:"status"`
	FailReason  string `gorm:"size:255" json:"fail_reason"`
	TxID        string `gorm:"column:txid;size:70;index" json:"txid"`
	// SignedRaw is the exact signed transaction. A retry always rebroadcasts
	// these bytes; a second transaction is only built after expiration and
	// only when the txid is provably absent from the chain.
	SignedRaw   string     `gorm:"type:text" json:"-"`
	ExpiredAt   *time.Time `json:"expired_at"`
	EnergyUsed  int64      `json:"energy_used"`
	FeeSun      int64      `json:"fee_sun"`
	BroadcastAt *time.Time `json:"broadcast_at"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (WithdrawRecord) TableName() string { return "withdraw_record" }

// SweepRecord moves a user deposit address balance into the finance wallet.
type SweepRecord struct {
	ID          int64      `gorm:"primaryKey" json:"id"`
	FromAddress string     `gorm:"size:64;index" json:"from_address"`
	ToAddress   string     `gorm:"size:64" json:"to_address"`
	Symbol      string     `gorm:"size:16" json:"symbol"`
	Contract    string     `gorm:"size:64" json:"contract"`
	AmountUnits string     `gorm:"type:decimal(38,0)" json:"amount_units"`
	Status      string     `gorm:"size:16;index" json:"status"`
	TxID        string     `gorm:"column:txid;size:70;index" json:"txid"`
	SignedRaw   string     `gorm:"type:text" json:"-"`
	FeeMode     string     `gorm:"size:24" json:"fee_mode"` // rent:<provider> | burn
	EnergyOrder string     `gorm:"size:64" json:"energy_order"`
	CostTRX     float64    `json:"cost_trx"`
	EnergyUsed  int64      `json:"energy_used"`
	FailReason  string     `gorm:"size:255" json:"fail_reason"`
	BroadcastAt *time.Time `json:"broadcast_at"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (SweepRecord) TableName() string { return "sweep_record" }

// EnergyRentOrder normalises orders across every rental provider.
type EnergyRentOrder struct {
	ID              int64      `gorm:"primaryKey" json:"id"`
	Provider        string     `gorm:"size:32;index" json:"provider"`
	RequestID       string     `gorm:"size:64;uniqueIndex" json:"request_id"`
	ProviderOrderID string     `gorm:"size:64;index" json:"provider_order_id"`
	ReceiveAddress  string     `gorm:"size:64;index" json:"receive_address"`
	ResourceType    string     `gorm:"size:16" json:"resource_type"`
	RequestedEnergy int64      `json:"requested_energy"`
	DelegatedEnergy int64      `json:"delegated_energy"`
	Period          string     `gorm:"size:16" json:"period"`
	CostTRX         float64    `json:"cost_trx"`
	Status          string     `gorm:"size:16;index" json:"status"`
	ProviderStatus  string     `gorm:"size:32" json:"provider_status"`
	DelegateTxID    string     `gorm:"column:delegate_txid;size:70" json:"delegate_txid"`
	Purpose         string     `gorm:"size:24" json:"purpose"` // sweep | hot_pool
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

func (EnergyRentOrder) TableName() string { return "energy_rent_order" }

// TopupRecord audits every prepaid balance refill, automatic or manual.
type TopupRecord struct {
	ID                int64      `gorm:"primaryKey" json:"id"`
	Provider          string     `gorm:"size:32;index" json:"provider"`
	RequestID         string     `gorm:"size:64;uniqueIndex" json:"request_id"`
	FromAddress       string     `gorm:"size:64" json:"from_address"`
	ToAddress         string     `gorm:"size:64" json:"to_address"`
	AmountTRX         float64    `json:"amount_trx"`
	TriggerBalanceTRX float64    `json:"trigger_balance_trx"`
	TxID              string     `gorm:"column:txid;size:70;index" json:"txid"`
	Status            string     `gorm:"size:16;index" json:"status"`
	Operator          string     `gorm:"size:32" json:"operator"` // auto | <user>
	FailReason        string     `gorm:"size:255" json:"fail_reason"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ConfirmedAt       *time.Time `json:"confirmed_at"`
}

func (TopupRecord) TableName() string { return "topup_record" }

// ChainCursor is the scanner position. block_hash detects reorgs.
//游标记录扫描进度,避免重复扫描区块
type ChainCursor struct {
	Name        string    `gorm:"size:32;primaryKey" json:"name"`
	BlockNumber int64     `json:"block_number"`
	BlockHash   string    `gorm:"size:70" json:"block_hash"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (ChainCursor) TableName() string { return "chain_cursor" }

// BlockSnapshot keeps recent block hashes so a fork point can be located.
//存区块哈希指纹，用于 reorg 检测/回溯分叉点
type BlockSnapshot struct {
	// The block height is the key, so it must not be auto assigned.
	BlockNumber int64     `gorm:"primaryKey;autoIncrement:false" json:"block_number"`
	BlockHash   string    `gorm:"size:70" json:"block_hash"`
	ParentHash  string    `gorm:"size:70" json:"parent_hash"`
	BlockTime   time.Time `json:"block_time"`
	CreatedAt   time.Time `json:"created_at"`
}

func (BlockSnapshot) TableName() string { return "block_snapshot" }

// NotifyOutbox is the transactional outbox. A record is written in the same
// transaction as the state change, then delivered at least once.
type NotifyOutbox struct {
	ID      int64  `gorm:"primaryKey" json:"id"`
	EventID string `gorm:"size:96;uniqueIndex" json:"event_id"`
	// MerchantID routes the event to that merchant's callback URL. Empty means
	// the event only goes to the platform wide publishers.
	MerchantID string     `gorm:"column:merchant_id;size:64;index" json:"merchant_id"`
	EventType  string     `gorm:"size:32;index" json:"event_type"`
	Payload    string     `gorm:"type:text" json:"payload"`
	Status     string     `gorm:"size:16;index" json:"status"`
	RetryCount int        `gorm:"size:32" json:"retry_count"`
	NextRetry  time.Time  `gorm:"index" json:"next_retry"`
	LastError  string     `gorm:"size:255" json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	SentAt     *time.Time `json:"sent_at"`
}

func (NotifyOutbox) TableName() string { return "notify_outbox" }

// SignAudit records every signing request. It never stores key material.
type SignAudit struct {
	ID        int64     `gorm:"primaryKey" json:"id"`
	Purpose   string    `gorm:"size:24;index" json:"purpose"`
	Path      string    `gorm:"size:64" json:"path"`
	Address   string    `gorm:"size:64;index" json:"address"`
	TxID      string    `gorm:"column:txid;size:70;index" json:"txid"`
	Caller    string    `gorm:"size:64" json:"caller"`
	Allowed   bool      `json:"allowed"`
	Reason    string    `gorm:"size:255" json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func (SignAudit) TableName() string { return "sign_audit" }

// AllModels is used by the migration helper.
func AllModels() []any {
	return []any{
		&Merchant{},
		&Wallet{},
		&WalletIndexAllocator{},
		&DepositRecord{},
		&WithdrawRecord{},
		&SweepRecord{},
		&EnergyRentOrder{},
		&TopupRecord{},
		&ChainCursor{},
		&BlockSnapshot{},
		&NotifyOutbox{},
		&SignAudit{},
	}
}
