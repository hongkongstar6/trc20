// Package signer isolates key material. Only cmd/sign links the server half;
// every other service talks to it over HTTP (mTLS in production) and only ever
// sends unsigned transactions.
package signer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/hd"
	"github.com/hongkongstar6/trc20/internal/tron"
)

// Purposes classify what a signature may be used for. sign-service enforces
// them, which is what caps the blast radius of a compromised worker.
const (
	PurposeWithdraw = "withdraw"
	PurposeSweep    = "sweep"
	PurposeTopup    = "topup"
)

// SignRequest is the wire format between workers and sign-service.
type SignRequest struct {
	Purpose string            `json:"purpose"`
	Path    string            `json:"path"`
	Address string            `json:"address"`
	Tx      *tron.Transaction `json:"tx"`
	// Meta carries the semantic intent the policy validates against the
	// transaction body (to address, amount, contract).
	Meta SignMeta `json:"meta"`
}

type SignMeta struct {
	ToAddress   string `json:"to_address"`
	Contract    string `json:"contract"`
	AmountUnits string `json:"amount_units"`
	AmountSun   int64  `json:"amount_sun"`
}

type SignResponse struct {
	TxID string            `json:"txid"` //交易哈希,客户端私钥签名后生成的整个数据进行哈希计算
	Tx   *tron.Transaction `json:"tx"`
}

// Policy is the server side allowlist. It is intentionally strict: the gas
// account may only send TRX to whitelisted provider deposit addresses.
type Policy struct {
	// TopupWhitelist maps provider name -> deposit address.
	TopupWhitelist    map[string]string
	TopupMaxSun       int64
	GasAccountAddress string
	// SweepDestination is the finance wallet; sweeps may go nowhere else.
	SweepDestination string
	// WithdrawFrom is the hot wallet address.
	WithdrawFrom string
	// AllowedContracts is the token contract allowlist.
	AllowedContracts map[string]bool
}

// Service derives keys and signs transactions that satisfy the policy.
type Service struct {
	wallet *hd.Wallet
	policy Policy
	audit  AuditSink
}

// AuditSink persists signing decisions. It never receives key material.
type AuditSink interface {
	Record(ctx context.Context, purpose, path, address, txid, caller string, allowed bool, reason string)
}

func New(cfg config.SignConfig, policy Policy, audit AuditSink) (*Service, error) {
	w, err := hd.NewFromMnemonic(cfg.Mnemonic, cfg.Passphrase)
	if err != nil {
		return nil, err
	}
	if policy.AllowedContracts == nil {
		policy.AllowedContracts = map[string]bool{}
	}
	return &Service{wallet: w, policy: policy, audit: audit}, nil
}

func (s *Service) recordAudit(ctx context.Context, req *SignRequest, txid, caller string, allowed bool, reason string) {
	if s.audit == nil {
		return
	}
	s.audit.Record(ctx, req.Purpose, req.Path, req.Address, txid, caller, allowed, reason)
}

// check enforces the per-purpose policy against the transaction contents.
func (s *Service) check(req *SignRequest) (string, error) {
	if req == nil || req.Tx == nil || req.Tx.RawDataHex == "" {
		return "empty tx", errors.New("signer: empty transaction")
	}
	if req.Path == "" {
		return "empty path", errors.New("signer: empty derivation path")
	}
	switch req.Purpose {
	case PurposeWithdraw:
		if s.policy.WithdrawFrom != "" && req.Address != s.policy.WithdrawFrom {
			return "withdraw from non hot wallet", fmt.Errorf("signer: withdrawals must originate from the hot wallet")
		}
		return s.checkContract(req)
	case PurposeSweep:
		if s.policy.SweepDestination != "" && req.Meta.ToAddress != s.policy.SweepDestination {
			return "sweep to non finance wallet", fmt.Errorf("signer: sweep destination %s is not the finance wallet", req.Meta.ToAddress)
		}
		return s.checkContract(req)
	case PurposeTopup:
		return s.checkTopup(req)
	default:
		return "unknown purpose", fmt.Errorf("signer: unknown purpose %q", req.Purpose)
	}
}

func (s *Service) checkContract(req *SignRequest) (string, error) {
	if len(s.policy.AllowedContracts) > 0 && !s.policy.AllowedContracts[req.Meta.Contract] {
		return "contract not allowed", fmt.Errorf("signer: contract %s is not allowed", req.Meta.Contract)
	}
	if req.Meta.ToAddress == "" || !tron.IsValidAddress(req.Meta.ToAddress) {
		return "invalid destination", errors.New("signer: invalid destination address")
	}
	amount, ok := new(big.Int).SetString(req.Meta.AmountUnits, 10)
	if !ok || amount.Sign() <= 0 {
		return "invalid amount", errors.New("signer: invalid amount")
	}
	// The declared intent must match the calldata that will actually execute.
	want, err := tron.EncodeTRC20Transfer(req.Meta.ToAddress, amount)
	if err != nil {
		return "encode failed", err
	}
	if !strings.Contains(strings.ToLower(req.Tx.RawDataHex), strings.ToLower(want)) {
		return "calldata mismatch", errors.New("signer: transaction body does not match the declared transfer")
	}
	return "", nil
}

// checkTopup is the tightest policy: TRX only, whitelisted destination only,
// hard per-transfer cap. This is the single automated outbound money path.
func (s *Service) checkTopup(req *SignRequest) (string, error) {
	if s.policy.GasAccountAddress == "" || req.Address != s.policy.GasAccountAddress {
		return "topup from non gas account", errors.New("signer: topups may only originate from the gas account")
	}
	allowed := false
	for _, addr := range s.policy.TopupWhitelist {
		if addr == req.Meta.ToAddress {
			allowed = true
			break
		}
	}
	if !allowed {
		return "topup destination not whitelisted", fmt.Errorf("signer: %s is not a whitelisted provider deposit address", req.Meta.ToAddress)
	}
	if req.Meta.AmountSun <= 0 || (s.policy.TopupMaxSun > 0 && req.Meta.AmountSun > s.policy.TopupMaxSun) {
		return "topup amount out of range", fmt.Errorf("signer: topup amount %d sun exceeds the per transfer cap", req.Meta.AmountSun)
	}
	if req.Meta.Contract != "" {
		return "topup must be plain trx", errors.New("signer: topups must be plain TRX transfers")
	}
	toHex, err := tron.AddressToHex(req.Meta.ToAddress)
	if err != nil {
		return "bad destination", err
	}
	if !strings.Contains(strings.ToLower(req.Tx.RawDataHex), strings.ToLower(toHex)) {
		return "topup body mismatch", errors.New("signer: transaction body does not contain the whitelisted destination")
	}
	if err := checkTRXTransferBody(req.Tx, req.Meta.AmountSun); err != nil {
		return "topup body mismatch", err
	}
	return "", nil
}

// checkTRXTransferBody verifies the parsed contract of a TRX transfer when the
// node echoed raw_data back to us.
func checkTRXTransferBody(tx *tron.Transaction, amountSun int64) error {
	if tx.RawData == nil {
		return nil
	}
	blob, err := json.Marshal(tx.RawData)
	if err != nil {
		return nil
	}
	var raw struct {
		Contract []struct {
			Type      string `json:"type"`
			Parameter struct {
				Value struct {
					Amount int64 `json:"amount"`
				} `json:"value"`
			} `json:"parameter"`
		} `json:"contract"`
	}
	if err := json.Unmarshal(blob, &raw); err != nil || len(raw.Contract) == 0 {
		return nil
	}
	c := raw.Contract[0]
	if c.Type != "TransferContract" {
		return fmt.Errorf("signer: expected TransferContract, got %s", c.Type)
	}
	if c.Parameter.Value.Amount != amountSun {
		return fmt.Errorf("signer: declared %d sun but the transaction moves %d sun", amountSun, c.Parameter.Value.Amount)
	}
	return nil
}

// Timeout used by the HTTP client half.
const defaultClientTimeout = 10 * time.Second
