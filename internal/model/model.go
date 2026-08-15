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
	// EnergyOrderAbandoned is an order the provider never resolved. It may have
	// been paid for, so it is reconciled against the provider statement by hand.
	EnergyOrderAbandoned = "abandoned"

	TopupStateCreated   = "created"
	TopupStateBroadcast = "broadcast"
	TopupStateConfirmed = "confirmed"
	TopupStateCredited  = "credited"
	TopupStateFailed    = "failed"

	MerchantStatusOff int8 = 0
	MerchantStatusOn  int8 = 1

	// ChainTRON is the only chain this gateway derives addresses and signs
	// transfers on. A merchant configured for another chain is served by
	// another gateway, so its address requests are refused here.
	ChainTRON = "TRON"
)

// Merchant is the tenant every user belongs to. Its secret signs both the
// inbound API parameters and the outbound deposit callbacks, so it never leaves
// the wallet system and is not serialised in API responses.
type Merchant struct {
	ID          int64  `gorm:"primaryKey" json:"id"`
	MerchantID  string `gorm:"column:merchant_id;size:30;uniqueIndex" json:"merchant_id"`
	Name        string `gorm:"size:64" json:"name"`
	CallbackURL string `gorm:"size:255" json:"callback_url"`
	// Symbol and Chain are what the merchant is opened for. An address request
	// carrying anything else is refused instead of being served an address on a
	// chain the merchant does not settle on.
	Symbol    string    `gorm:"size:16" json:"symbol"` // usdt | usdc
	Chain     string    `gorm:"size:16" json:"chain"`  // tron | eth | ...
	Secret    string    `gorm:"size:128" json:"-"`     // sha256 signing key
	Status    int8      `json:"status"`                // 1 enabled | 0 disabled
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Merchant) TableName() string { return "merchant" }

// Wallet is a derived address. Private keys never live here: only the
// derivation path, which is meaningless without the seed held by sign-service.
type UserWallet struct {
	ID         int64  `gorm:"primaryKey" json:"id"`
	MerchantID string `gorm:"column:merchant_id;size:30;index" json:"merchant_id"`
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

func (UserWallet) TableName() string { return "user_wallet" }

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
// 光靠游标还不够，因为进程可能在扫到块、但还没保存游标之前就崩溃/重启，导致同一个块被重扫。为此，DepositRecord 表上以 (txid, event_index) 建了唯一索引 uq_tx_event
type DepositRecord struct {
	ID            int64      `gorm:"primaryKey" json:"id"`
	MerchantID    string     `gorm:"column:merchant_id;size:30;index" json:"merchant_id"`
	Account       string     `gorm:"column:account;index" json:"account"`
	Uid           string     `gorm:"column:uid" json:"uid"`
	TradeNo       string     `gorm:"size:64;uniqueIndex" json:"trade_no"` //交易订单号(我方生成订单号)
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

// WithdrawRecord holds one business withdrawal order. order_no is unique,
// which is what guarantees "at most one on-chain transfer per business order".
// 提现订单
type WithdrawRecord struct {
	ID          int64  `gorm:"primaryKey" json:"id"`
	OrderNo     string `gorm:"size:64;uniqueIndex" json:"order_no"`     //商户订单号(商户生成订单号)
	TradeNo     string `gorm:"size:64;uniqueIndex" json:"trade_no"`     //交易订单号(我方生成订单号)
	ExtParam    string `gorm:"column:ext_param;index" json:"ext_param"` //拓展字段，原样返回，一般存用户id
	MerchantID  string `gorm:"column:merchant_id;size:30" json:"merchant_id"`
	Chain       string `gorm:"size:16" json:"chain"`            //公链类型，ETH / TRON / BSC / BTC / SOL
	Symbol      string `gorm:"size:16" json:"symbol"`           //币种,usdt,udsc
	Contract    string `gorm:"size:64" json:"contract"`         //
	FromAddress string `gorm:"size:64" json:"from_address"`     //付款地址
	ToAddress   string `gorm:"size:64;index" json:"to_address"` //收款地址
	// NotifyURL is the per-order callback URL the business system submitted with
	// the order. The final outcome of the order is always posted to it.
	NotifyURL   string `gorm:"column:notify_url;size:255" json:"notify_url"`
	AmountUnits string `gorm:"type:decimal(38,0)" json:"amount_units"`
	Decimals    int    `gorm:"size:32" json:"decimals"`
	Status      string `gorm:"size:16;index" json:"status"`
	FailReason  string `gorm:"size:255" json:"fail_reason"`
	// FailCode is the classified reason (chain.Fail*), which is what retry and
	// alerting branch on; FailReason keeps the raw node message.
	FailCode string `gorm:"size:32;index" json:"fail_code"`
	// HaltCount counts the rounds the order was halted before signing, e.g. the
	// hot wallet being short of USDT or TRX. Once it reaches
	// withdraw_server.halt_max_retries the order is failed instead of retried.
	// 提现因人工原因停下的次数，达到配置上限后订单结单为失败
	HaltCount int    `gorm:"column:halt_count;default:0" json:"halt_count"`
	TxID      string `gorm:"column:txid;size:70;index" json:"txid"` //交易的链hash
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
	ExpiredAt   *time.Time `json:"expired_at"`
	FeeMode     string     `gorm:"size:24" json:"fee_mode"` // rent:<provider> | burn
	EnergyOrder string     `gorm:"size:64" json:"energy_order"`
	CostTRX     float64    `json:"cost_trx"`
	EnergyUsed  int64      `json:"energy_used"`
	FailReason  string     `gorm:"size:255" json:"fail_reason"`
	FailCode    string     `gorm:"size:32;index" json:"fail_code"`
	// RetryCount is how many times this address already failed the same way
	// before this attempt, which drives the energy safety factor.
	RetryCount int `gorm:"size:32" json:"retry_count"`
	// DepositMaxID is the highest deposit_record id this sweep covers. Only
	// those rows are marked swept, so a deposit that confirms while the sweep is
	// in flight is not written off with it.
	DepositMaxID int64      `gorm:"column:deposit_max_id" json:"deposit_max_id"`
	BroadcastAt  *time.Time `json:"broadcast_at"`
	ConfirmedAt  *time.Time `json:"confirmed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (SweepRecord) TableName() string { return "sweep_record" }

// SweepSkip counts the consecutive rounds an address was left alone for
// holding less than the minimum sweep amount. Once the count reaches
// sweep_server.threshold.max_skip_rounds the address is swept anyway, so a
// wallet that never grows past the threshold still reaches the finance
// wallet. The row is deleted as soon as the address is swept.
type SweepSkip struct {
	Address string `gorm:"size:64;primaryKey" json:"address"`
	// Contract is the token the counter belongs to: one address holds one
	// balance per token and each of them crosses the threshold on its own.
	Contract   string    `gorm:"size:64;primaryKey" json:"contract"`
	SkipCount  int       `json:"skip_count"`
	LastSkipAt time.Time `json:"last_skip_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (SweepSkip) TableName() string { return "sweep_skip" }

// AddressBlacklist lists the destination addresses withdrawals must never pay
// out to. It lives in the database so the list can be edited without a redeploy.
// 地址黑名单
type AddressBlacklist struct {
	ID      int64     `gorm:"primaryKey" json:"id"`
	Address string    `gorm:"size:64;uniqueIndex" json:"address"`
	AddTime time.Time `gorm:"column:add_time" json:"add_time"`
	Account string    `gorm:"column:account;size:128;not null;default:''" json:"account"`
}

func (AddressBlacklist) TableName() string { return "address_blacklist" }

// EnergyRentOrder normalises orders across every rental provider.
type EnergyRentOrder struct {
	ID              int64  `gorm:"primaryKey" json:"id"`
	Provider        string `gorm:"size:32;index" json:"provider"`
	RequestID       string `gorm:"size:64;uniqueIndex" json:"request_id"`
	ProviderOrderID string `gorm:"size:64;index" json:"provider_order_id"`
	ReceiveAddress  string `gorm:"size:64;index" json:"receive_address"`
	ResourceType    string `gorm:"size:16" json:"resource_type"`
	RequestedEnergy int64  `json:"requested_energy"`
	DelegatedEnergy int64  `json:"delegated_energy"`
	// BaselineEnergy is the energy the receiving address could already spend
	// when the order was placed. The delegation is confirmed against this
	// baseline instead of the absolute balance.
	BaselineEnergy int64      `json:"baseline_energy"`
	Period         string     `gorm:"size:16" json:"period"`
	CostTRX        float64    `json:"cost_trx"`
	Status         string     `gorm:"size:16;index" json:"status"`
	ProviderStatus string     `gorm:"size:32" json:"provider_status"`
	DelegateTxID   string     `gorm:"column:delegate_txid;size:70" json:"delegate_txid"`
	Purpose        string     `gorm:"size:24" json:"purpose"` // sweep | hot_pool
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	FinishedAt     *time.Time `json:"finished_at"`
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
// 游标记录扫描进度,避免重复扫描区块
type ChainCursor struct {
	Name        string    `gorm:"size:32;primaryKey" json:"name"`
	BlockNumber int64     `json:"block_number"`
	BlockHash   string    `gorm:"size:70" json:"block_hash"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (ChainCursor) TableName() string { return "chain_cursor" }

// BlockSnapshot keeps recent block hashes so a fork point can be located.
// 存区块哈希指纹，用于 reorg 检测/回溯分叉点
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
	MerchantID string `gorm:"column:merchant_id;size:30;index" json:"merchant_id"`
	Account    string `gorm:"column:account;size:64;index" json:"account"`
	// NotifyURL overrides the merchant callback URL for this single event: a
	// withdrawal is reported to the notify_url of its own order.
	NotifyURL  string     `gorm:"column:notify_url;size:255" json:"notify_url"`
	EventType  string     `gorm:"size:32;index" json:"event_type"`
	Payload    string     `gorm:"type:text" json:"payload"`
	Status     string     `gorm:"size:16;index" json:"status"` //发送状态
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
		&UserWallet{},
		&WalletIndexAllocator{},
		&DepositRecord{},
		&WithdrawRecord{},
		&SweepRecord{},
		&SweepSkip{},
		&AddressBlacklist{},
		&EnergyRentOrder{},
		&TopupRecord{},
		&ChainCursor{},
		&BlockSnapshot{},
		&NotifyOutbox{},
		&SignAudit{},
	}
}
