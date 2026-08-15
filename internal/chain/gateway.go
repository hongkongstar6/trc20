// Package chain implements the node gateway: a priority ordered list of TRON
// nodes (self hosted FullNode and/or TronGrid) with automatic failover.
//
// Broadcast has one extra rule that the rest of the system depends on: on a
// network level failure the *same signed bytes* are sent to the next node.
// A second transaction is never constructed here, because a timeout does not
// mean the first node dropped the transaction.
package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/tron"
	"github.com/sirupsen/logrus"
)

// ErrNoNodeAvailable means every configured node failed. Callers must stop
// rather than skip work (skipping a block would silently lose deposits).
var ErrNoNodeAvailable = errors.New("chain: no node available")

// Gateway routes JSON HTTP calls to the healthiest node by priority.
type Gateway struct {
	nodes            []*node
	retryPerNode     int
	solidityConfirm  bool
	broadcastTimeout time.Duration
	rateLimitWait    time.Duration
	// rateLimitWait bounds how long a call may sit waiting for a throttled
	// node to reopen. Waiting is correct for the scanner (skipping a block
	// loses deposits), but it must not block a caller forever.
}
type node struct {
	conf    config.NodeConfig
	client  *http.Client
	limiter *limiter
	cool    cooldown
}

// defaultTronGridQPS stays under the 15 req/s of a free TronGrid key: the key
// is suspended for tens of seconds once the limit is hit, which costs far more
// throughput than the few requests per second given up here.
const defaultTronGridQPS = 10

func NewGateway(c config.ChainConfig) (*Gateway, error) {
	g := &Gateway{
		retryPerNode:     max(c.RetryPerNode, 1),
		solidityConfirm:  c.SolidityForConfirm,
		broadcastTimeout: config.Duration(c.BroadcastTimeout, 20*time.Second),
		rateLimitWait:    config.Duration(c.RateLimitWait, 60*time.Second),
	}
	for _, nc := range c.ChainNodes {
		if !nc.Enabled {
			continue
		}
		qps := nc.QPS
		if qps == 0 && strings.EqualFold(nc.Type, "trongrid") {
			qps = defaultTronGridQPS
		}
		g.nodes = append(g.nodes, &node{
			conf:    nc,
			client:  &http.Client{Timeout: config.Duration(nc.Timeout, 15*time.Second)},
			limiter: newLimiter(qps, nc.Burst),
		})
	}
	if len(g.nodes) == 0 {
		return nil, ErrNoNodeAvailable
	}
	sort.SliceStable(g.nodes, func(i, j int) bool {
		return g.nodes[i].conf.Priority < g.nodes[j].conf.Priority
	})
	return g, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// call posts body to path on each node in priority order until one answers.
func (g *Gateway) call(ctx context.Context, path string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	var lastErr error
	giveUp := time.Now().Add(g.rateLimitWait)
	for {
		var reopen time.Duration // shortest cooldown left across all nodes
		for _, n := range g.nodes {
			if left := n.cool.remaining(); left > 0 {
				if reopen == 0 || left < reopen {
					reopen = left
				}
				continue
			}
			for attempt := 0; attempt < g.retryPerNode; attempt++ {
				raw, err := n.do(ctx, path, payload)
				if err != nil {
					lastErr = fmt.Errorf("%s: %w", n.conf.Name, err)
					if errors.Is(err, errRateLimited) {
						// Retrying the same node now would only deepen the
						// suspension; it is parked until its cooldown ends.
						if left := n.cool.remaining(); left > 0 && (reopen == 0 || left < reopen) {
							reopen = left
						}
						break
					}
					continue
				}
				if out == nil {
					return nil
				}
				if err := json.Unmarshal(raw, out); err != nil {
					lastErr = fmt.Errorf("%s: decode %s: %w", n.conf.Name, path, err)
					continue
				}
				return nil
			}
		}
		if reopen <= 0 {
			break // every failure was a real node failure
		}
		// Only throttling is left: wait for the first node to reopen rather
		// than reporting an outage, so the caller does not skip work.
		if time.Now().Add(reopen).After(giveUp) {
			break
		}
		t := time.NewTimer(reopen + 100*time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	if lastErr == nil {
		lastErr = ErrNoNodeAvailable
	}
	return fmt.Errorf("%w: %v", ErrNoNodeAvailable, lastErr)
}

func (n *node) do(ctx context.Context, path string, payload []byte) ([]byte, error) {
	url := strings.TrimRight(n.conf.Endpoint, "/") + path
	var reader io.Reader
	method := http.MethodGet
	if payload != nil {
		reader = bytes.NewReader(payload)
		method = http.MethodPost
	}
	//logrus.Debugf("node_name:%s method:%s url:%s", n.conf.Name, method, url)
	if n.limiter != nil {
		if err := n.limiter.wait(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.conf.APIKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", n.conf.APIKey) //扫描波场官方节点需要的key
	}
	for k, v := range n.conf.Headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		body := truncate(string(raw), 200)
		retryAfter := parseRetryAfter(resp.Header, string(raw))
		n.cool.set(retryAfter)
		logrus.Warnf("node_name:%s rate limited, backing off %s, body:%s", n.conf.Name, retryAfter, body)
		return nil, &rateLimitError{body: body}
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return raw, nil
}

// truncate shortens a node response for logging. The cut is moved back to a
// rune boundary: a body cut inside a multi byte character would put invalid
// UTF-8 into the log file, which makes editors treat the file as binary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------- block data

type Block struct {
	BlockID     string `json:"blockID"`
	BlockHeader struct {
		RawData struct {
			Number         int64  `json:"number"`
			Timestamp      int64  `json:"timestamp"`
			ParentHash     string `json:"parentHash"`
			WitnessAddress string `json:"witness_address"`
		} `json:"raw_data"`
	} `json:"block_header"`
	Transactions []struct {
		TxID string `json:"txID"`
		Ret  []struct {
			ContractRet string `json:"contractRet"`
		} `json:"ret"`
	} `json:"transactions"`
}

type BlockDetail struct {
	BlockID     string `json:"blockID"`
	BlockHeader struct {
		RawData struct {
			Timestamp      int64  `json:"timestamp"`
			TxTrieRoot     string `json:"txTrieRoot"`
			ParentHash     string `json:"parentHash"`
			Number         int    `json:"number"`
			WitnessAddress string `json:"witness_address"`
			Version        int    `json:"version"`
		} `json:"raw_data"`
		WitnessSignature string `json:"witness_signature"`
	} `json:"block_header"`
	Transactions []struct {
		RawData struct {
			RefBlockBytes string `json:"ref_block_bytes"`
			RefBlockHash  string `json:"ref_block_hash"`
			Expiration    int64  `json:"expiration"`
			Contract      []struct {
				Parameter struct {
					Value struct {
						OwnerAddress    string `json:"owner_address"`
						Resource        string `json:"resource"`
						Balance         int64  `json:"balance"`
						ReceiverAddress string `json:"receiver_address"`
					} `json:"value"`
					TypeURL string `json:"type_url"`
				} `json:"parameter"`
				Type         string `json:"type"`
				PermissionID int    `json:"Permission_id"`
			} `json:"contract"`
			Timestamp int64 `json:"timestamp"`
		} `json:"raw_data"`
		Signature []string `json:"signature"`
		Ret       []struct {
			ContractRet string `json:"contractRet"`
		} `json:"ret"`
		RawDataHex string `json:"raw_data_hex"`
		TxID       string `json:"txID"`
	} `json:"transactions"`
}

func (b *Block) Number() int64    { return b.BlockHeader.RawData.Number }
func (b *Block) Timestamp() int64 { return b.BlockHeader.RawData.Timestamp }

// walletPath returns the confirmed (solidity) path when the caller asked for
// finalised data and the deployment is configured for it.
func (g *Gateway) walletPath(p string, solidity bool) string {
	if solidity && g.solidityConfirm {
		return "/walletsolidity" + p
	}
	return "/wallet" + p
}

func (g *Gateway) GetNowBlock(ctx context.Context) (*Block, error) {
	var b Block
	if err := g.call(ctx, g.walletPath("/getnowblock", false), map[string]any{}, &b); err != nil {
		return nil, err
	}
	if b.BlockID == "" {
		return nil, errors.New("empty block returned")
	}
	return &b, nil
}

func (g *Gateway) GetBlockByNum(ctx context.Context, num int64) (*Block, error) {
	var b Block
	if err := g.call(ctx, g.walletPath("/getblockbynum", false), map[string]any{"num": num}, &b); err != nil {
		return nil, err
	}
	if b.BlockID == "" {
		return nil, fmt.Errorf("block %d not found", num)
	}
	return &b, nil
}

// TxInfo is the receipt style structure returned by gettransactioninfo*.
type TxInfo struct {
	ID              string `json:"id"`
	BlockNumber     int64  `json:"blockNumber"`
	BlockTimeStamp  int64  `json:"blockTimeStamp"`
	ContractAddress string `json:"contract_address"`
	Fee             int64  `json:"fee"`
	Receipt         struct {
		EnergyUsageTotal int64  `json:"energy_usage_total"`
		NetUsage         int64  `json:"net_usage"`
		Result           string `json:"result"`
	} `json:"receipt"`
	Log        []TxLog `json:"log"` //里面含有
	Result     string  `json:"result"`
	ResMessage string  `json:"resMessage"`
}

// TxLog is one contract event log inside a transaction info entry.
type TxLog struct {
	Address string   `json:"address"` //抛出该事件的智能合约地址 hex, 41 prefixed
	Topics  []string `json:"topics"`  //事件的签名 Hash（topics[0]）以及被 indexed 标记的参数
	Data    string   `json:"data"`
}

// Succeeded reports whether the contract execution itself succeeded. A failed
// TRC20 transfer still lands on chain and still emits a receipt, so this check
// is what prevents fake deposits.
func (t *TxInfo) Succeeded() bool {
	if t.Result == "FAILED" {
		return false
	}
	r := t.Receipt.Result
	return r == "" || r == "SUCCESS"
}

// GetTxInfoByBlockNum returns every transaction receipt in a block. This is
// the primary source for deposit scanning: one call per block.
func (g *Gateway) GetTxInfoByBlockNum(ctx context.Context, num int64) ([]TxInfo, error) {
	var infos []TxInfo
	err := g.call(ctx, g.walletPath("/gettransactioninfobyblocknum", false), map[string]any{"num": num}, &infos)
	return infos, err
}

func (g *Gateway) GetTxInfoByID(ctx context.Context, txid string) (*TxInfo, error) {
	return g.txInfoByID(ctx, txid, false)
}

// GetTxInfoByIDConfirmed reads the receipt from the solidity node, which only
// serves solidified blocks. A transaction sitting in an unsolidified block, or
// one a fork dropped, is reported as absent instead of as an outcome that a
// reorg could still flip. It falls back to the full node when the deployment
// has chain.solidity_for_confirm disabled.
func (g *Gateway) GetTxInfoByIDConfirmed(ctx context.Context, txid string) (*TxInfo, error) {
	return g.txInfoByID(ctx, txid, true)
}

func (g *Gateway) txInfoByID(ctx context.Context, txid string, solidity bool) (*TxInfo, error) {
	var info TxInfo
	if err := g.call(ctx, g.walletPath("/gettransactioninfobyid", solidity), map[string]any{"value": txid}, &info); err != nil {
		return nil, err
	}
	if info.ID == "" {
		return nil, nil // not on chain (yet)
	}
	return &info, nil
}

// SolidityConfirm reports whether finality is taken from the solidity node.
func (g *Gateway) SolidityConfirm() bool { return g.solidityConfirm }

// --------------------------------------------------------------- contract IO
// 调用接口 https://api.trongrid.io/wallet/triggersmartcontract 返回的完整数据格式
type TriggerResultDetail struct {
	Result struct {
		Result bool `json:"result"`
	} `json:"result"`
	EnergyUsed     int      `json:"energy_used"`
	EnergyPenalty  int      `json:"energy_penalty"`
	ConstantResult []string `json:"constant_result"`
	Transaction    struct {
		Visible bool   `json:"visible"` //
		TxID    string `json:"txID"`
		RawData struct {
			Contract []struct {
				Parameter struct {
					Value struct {
						Data            string `json:"data"`
						OwnerAddress    string `json:"owner_address"`
						ContractAddress string `json:"contract_address"`
					} `json:"value"`
					TypeURL string `json:"type_url"`
				} `json:"parameter"`
				Type string `json:"type"`
			} `json:"contract"`
			RefBlockBytes string `json:"ref_block_bytes"`
			RefBlockHash  string `json:"ref_block_hash"`
			Expiration    int64  `json:"expiration"`
			FeeLimit      int    `json:"fee_limit"`
			Timestamp     int64  `json:"timestamp"`
		} `json:"raw_data"`
		RawDataHex string `json:"raw_data_hex"`
	} `json:"transaction"`
}

type triggerResult struct {
	Result struct {
		Result  bool   `json:"result"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"result"`
	ConstantResult []string          `json:"constant_result"`
	Transaction    *tron.Transaction `json:"transaction"`
	EnergyUsed     int64             `json:"energy_used"` //本次交易需要的能量,但是不会显示消耗的带宽量
}

// TriggerConstantContract performs a read-only contract call and also returns
// the energy the same call would consume, which the fee estimator relies on.
// 预估转账需要的能量
func (g *Gateway) TriggerConstantContract(ctx context.Context, owner, contract, data string) (result string, energy int64, err error) {
	ownerHex, err := tron.AddressToHex(owner)
	if err != nil {
		return "", 0, err
	}
	contractHex, err := tron.AddressToHex(contract)
	if err != nil {
		return "", 0, err
	}
	var out triggerResult
	// data already carries the selector, so function_selector is omitted: the
	// node rejects a request that sets both.
	body := map[string]any{
		"owner_address":    ownerHex,
		"contract_address": contractHex,
		"data":             data,
	}
	//预估交易要消耗的能量
	if err := g.call(ctx, g.walletPath("/triggerconstantcontract", false), body, &out); err != nil {
		return "", 0, err
	}
	if !out.Result.Result && out.Result.Code != "" {
		return "", 0, fmt.Errorf("constant call failed: %s %s", out.Result.Code, decodeHexMessage(out.Result.Message))
	}
	if len(out.ConstantResult) == 0 {
		return "", out.EnergyUsed, nil
	}
	return out.ConstantResult[0], out.EnergyUsed, nil
}

// BuildTRC20Transfer asks the node to assemble an unsigned transfer. The
// caller signs it in sign-service; this process never sees a private key.
// 请求节点组装一个未签名的转账
// 负责帮你查好最新区块号、打包好复杂的合约指令、评估消耗、并模拟跑一遍看看确认没问题，最后打包成一个标准的Transaction JSON
func (g *Gateway) BuildTRC20Transfer(ctx context.Context, owner, contract, data string, feeLimit int64) (*tron.Transaction, error) {
	ownerHex, err := tron.AddressToHex(owner)
	if err != nil {
		return nil, err
	}
	contractHex, err := tron.AddressToHex(contract)
	if err != nil {
		return nil, err
	}
	var out triggerResult
	body := map[string]any{
		"owner_address":    ownerHex,
		"contract_address": contractHex,
		"data":             data,
		"fee_limit":        feeLimit,
		"call_value":       0,
	}
	// 在 TRON 网络中，所有与智能合约相关的交互（例如：查询 TRC-20 代币余额、发起 USDT 转账、调用 DApp 合约函数等）
	// 都可以通过这个接口来完成
	// 负责帮你查好最新区块号、打包好复杂的合约指令、并模拟跑一遍确认没问题，最后打包成一个标准的 Transaction JSON
	if err := g.call(ctx, g.walletPath("/triggersmartcontract", false), body, &out); err != nil {
		return nil, err
	}
	if out.Transaction == nil {
		return nil, fmt.Errorf("build transfer failed: %s %s", out.Result.Code, decodeHexMessage(out.Result.Message))
	}
	return out.Transaction, nil
}

// BuildTRXTransfer assembles a plain TRX transfer (used by the gas account
// when refilling rental provider prepaid balances).
func (g *Gateway) BuildTRXTransfer(ctx context.Context, from, to string, amountSun int64) (*tron.Transaction, error) {
	fromHex, err := tron.AddressToHex(from)
	if err != nil {
		return nil, err
	}
	toHex, err := tron.AddressToHex(to)
	if err != nil {
		return nil, err
	}
	var tx tron.Transaction
	body := map[string]any{"owner_address": fromHex, "to_address": toHex, "amount": amountSun}
	if err := g.call(ctx, g.walletPath("/createtransaction", false), body, &tx); err != nil {
		return nil, err
	}
	if tx.RawDataHex == "" {
		return nil, errors.New("node returned an empty transaction")
	}
	return &tx, nil
}

// ------------------------------------------------------------------ accounts

type AccountResource struct {
	FreeNetLimit      int64 `json:"freeNetLimit"`
	FreeNetUsed       int64 `json:"freeNetUsed"`
	NetLimit          int64 `json:"NetLimit"`
	NetUsed           int64 `json:"NetUsed"`
	EnergyLimit       int64 `json:"EnergyLimit"`
	EnergyUsed        int64 `json:"EnergyUsed"`
	TotalEnergyLimit  int64 `json:"TotalEnergyLimit"`
	TotalEnergyWeight int64 `json:"TotalEnergyWeight"`
}

// AvailableEnergy is what is usable right now, including delegated energy.
func (a *AccountResource) AvailableEnergy() int64 {
	if a.EnergyLimit <= a.EnergyUsed {
		return 0
	}
	return a.EnergyLimit - a.EnergyUsed
}

// AvailableBandwidth includes the daily free quota.
func (a *AccountResource) AvailableBandwidth() int64 {
	free := a.FreeNetLimit - a.FreeNetUsed
	staked := a.NetLimit - a.NetUsed
	total := free + staked
	if total < 0 {
		return 0
	}
	return total
}

func (g *Gateway) GetAccountResource(ctx context.Context, addr string) (*AccountResource, error) {
	h, err := tron.AddressToHex(addr)
	if err != nil {
		return nil, err
	}
	var res AccountResource
	if err := g.call(ctx, g.walletPath("/getaccountresource", false), map[string]any{"address": h}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

type account struct {
	Address string `json:"address"`
	Balance int64  `json:"balance"`
}

// GetTRXBalance returns the balance in SUN. A never activated account returns 0.
func (g *Gateway) GetTRXBalance(ctx context.Context, addr string) (int64, error) {
	h, err := tron.AddressToHex(addr)
	if err != nil {
		return 0, err
	}
	var acc account
	if err := g.call(ctx, g.walletPath("/getaccount", false), map[string]any{"address": h}, &acc); err != nil {
		return 0, err
	}
	return acc.Balance, nil
}

// ChainParameters exposes the on-chain energy/bandwidth prices used by the
// break-even calculation, so nothing is hard coded.
type ChainParameters struct {
	EnergyFeeSun      int64
	TransactionFeeSun int64
}

func (g *Gateway) GetChainParameters(ctx context.Context) (*ChainParameters, error) {
	var out struct {
		ChainParameter []struct {
			Key   string `json:"key"`
			Value int64  `json:"value"`
		} `json:"chainParameter"`
	}
	if err := g.call(ctx, g.walletPath("/getchainparameters", false), nil, &out); err != nil {
		return nil, err
	}
	p := &ChainParameters{EnergyFeeSun: 100, TransactionFeeSun: 1000}
	for _, kv := range out.ChainParameter {
		switch kv.Key {
		case "getEnergyFee":
			p.EnergyFeeSun = kv.Value
		case "getTransactionFee":
			p.TransactionFeeSun = kv.Value
		}
	}
	return p, nil
}

// ----------------------------------------------------------------- broadcast

type broadcastResult struct {
	Result  bool   `json:"result"`
	Code    string `json:"code"`
	Message string `json:"message"`
	TxID    string `json:"txid"`
}

// BroadcastResult tells the caller whether the transaction is on its way.
type BroadcastResult struct {
	TxID       string
	Accepted   bool
	Duplicated bool // the node already knows this transaction
	Code       string
	Message    string
}

// broadcastRequest picks the endpoint that fits what the caller has.
//
// /wallet/broadcasttransaction parses the raw_data JSON object and ignores
// raw_data_hex, so a transaction rebuilt from a stored raw_data_hex alone (the
// rebroadcast path) is read by the node as an empty transaction and answered
// with a bare {"result": false}. Those bytes are sent as protobuf on
// /wallet/broadcasthex instead, which needs nothing but raw_data_hex and the
// signature.
func broadcastRequest(tx *tron.Transaction) (path string, payload []byte, err error) {
	if tx != nil && len(tx.RawData) == 0 {
		hexTx, err := tron.SerializeSigned(tx)
		if err != nil {
			return "", nil, err
		}
		payload, err = json.Marshal(map[string]string{"transaction": hexTx})
		if err != nil {
			return "", nil, err
		}
		return "/wallet/broadcasthex", payload, nil
	}
	payload, err = json.Marshal(tx)
	if err != nil {
		return "", nil, err
	}
	return "/wallet/broadcasttransaction", payload, nil
}

// Broadcast sends one already signed transaction. The same bytes are retried
// against the fallback nodes; the transaction is never rebuilt here.
func (g *Gateway) Broadcast(ctx context.Context, tx *tron.Transaction) (*BroadcastResult, error) {
	ctx, cancel := context.WithTimeout(ctx, g.broadcastTimeout)
	defer cancel()

	path, payload, err := broadcastRequest(tx)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, n := range g.nodes {
		if left := n.cool.remaining(); left > 0 {
			lastErr = fmt.Errorf("%s: rate limited for another %s", n.conf.Name, left.Truncate(time.Second))
			continue
		}
		raw, err := n.do(ctx, path, payload)
		if err != nil {
			// Network level failure: we cannot tell whether the node accepted
			// the transaction, so we try the next node with identical bytes.
			lastErr = fmt.Errorf("%s: %w", n.conf.Name, err)
			continue
		}
		var res broadcastResult
		if err := json.Unmarshal(raw, &res); err != nil {
			lastErr = fmt.Errorf("%s: decode broadcast: %w", n.conf.Name, err)
			continue
		}
		out := &BroadcastResult{TxID: tx.TxID, Code: res.Code, Message: decodeHexMessage(res.Message)}
		switch {
		case res.Result:
			out.Accepted = true
		case isDuplicate(res.Code, out.Message):
			out.Accepted, out.Duplicated = true, true
		default:
			reason := strings.TrimSpace(res.Code + " " + out.Message)
			if reason == "" {
				// A bare {"result": false} carries no reason; say so instead of
				// logging an empty error that looks like a bug.
				reason = "unknown reason (" + n.conf.Name + ")"
			}
			return out, fmt.Errorf("broadcast rejected: %s", reason)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: %v", ErrNoNodeAvailable, lastErr)
}

func isDuplicate(code, message string) bool {
	if strings.ToUpper(code) == "DUP_TRANSACTION_ERROR" {
		return true
	}
	m := strings.ToLower(message)
	return strings.Contains(m, "dup transaction") || strings.Contains(m, "already exists")
}

func decodeHexMessage(msg string) string {
	if msg == "" {
		return ""
	}
	if b, err := hexDecode(msg); err == nil && isPrintable(b) {
		return string(b)
	}
	return msg
}

func hexDecode(s string) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	if len(s)%2 != 0 {
		return nil, errors.New("odd length")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		var v int
		for j := 0; j < 2; j++ {
			c := s[i*2+j]
			switch {
			case c >= '0' && c <= '9':
				v = v<<4 | int(c-'0')
			case c >= 'a' && c <= 'f':
				v = v<<4 | int(c-'a'+10)
			case c >= 'A' && c <= 'F':
				v = v<<4 | int(c-'A'+10)
			default:
				return nil, errors.New("not hex")
			}
		}
		out[i] = byte(v)
	}
	return out, nil
}

func isPrintable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return len(b) > 0
}
