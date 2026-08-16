// Package transfer holds the on-chain half that sweeping and withdrawing
// share. Moving a deposit balance into the finance wallet and paying out a
// withdrawal order are the same TRC20 transfer, executed in the same order:
//
//  1. estimate the energy with a read-only constant call, which carries no
//     expiration and does not need the address to hold any energy at all;
//  2. make that energy available (rent it, or verify the address can burn its
//     own TRX for it) before the real transaction exists;
//  3. assemble the transaction, sign it and broadcast it immediately, so the
//     expiration window the node writes into it is never spent waiting.
//
// Only those steps live here. What a sweep and a withdrawal do around them is
// deliberately different - their own tables and state machines, their own risk
// rules, their own energy policy (a sweep may burn TRX, a withdrawal never
// does) and their own idea of when a transfer is final - so each caller keeps
// that logic and injects it through Hooks.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/energy"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/hongkongstar6/trc20/internal/tron"
	"github.com/sirupsen/logrus"
)

// ErrStop ends a transfer without reporting a failure. A hook returns it when
// the transfer must not continue but nothing went wrong: the address cannot
// afford the fee this round, or another worker already took the row.
var ErrStop = errors.New("transfer: stopped by the caller")

// Executor performs transfers with a node gateway, the sign service and the
// energy manager. It holds no state of its own, so one instance is shared by
// every goroutine of a service.
type Executor struct {
	gw   *chain.Gateway
	sign *signer.Client
	mgr  *energy.Manager
}

func NewExecutor(gw *chain.Gateway, sign *signer.Client, mgr *energy.Manager) *Executor {
	return &Executor{gw: gw, sign: sign, mgr: mgr}
}

// Request is one TRC20 transfer. Data is the encoded transfer payload: the
// caller encodes it itself because an unencodable destination is a caller-side
// rejection, decided before any of these steps run.
type Request struct {
	// Purpose is the signer purpose the sign service validates the transfer
	// against, e.g. signer.PurposeSweep.
	Purpose string
	// From pays, DerivePath is its BIP44 path in the sign service.
	From       string
	DerivePath string
	// To, Contract and Amount are what Data encodes; they are repeated here for
	// the signing policy, which checks the intent against the transaction body.
	To          string
	Contract    string
	Amount      *big.Int
	Data        string
	FeeLimitSun int64
	// EnergyFactor is the head room added to the estimate. Zero means
	// energy.DefaultSafetyFactor.
	EnergyFactor float64
	// FallbackEnergy is used when the estimate itself fails: a node error must
	// not stop a transfer that can run on a worst-case number.
	FallbackEnergy int64
	// ExpirationSec mirrors the node's transaction expiration, which is how long
	// the signed bytes stay broadcastable.
	ExpirationSec int64
}

// Signed is the transaction the sign service returned, before it was broadcast.
type Signed struct {
	TxID       string
	RawDataHex string
	// ExpiredAt is when the signed bytes stop being broadcastable, so a caller
	// can tell an expired transfer from one that is still in flight.
	ExpiredAt time.Time
	// Energy is the amount this transfer was prepared for.
	Energy int64

	tx *tron.Transaction
}

// Broadcast is the outcome of sending the signed bytes to a node. A rejection
// classified as permanent can never be accepted, so the caller settles the row
// now instead of rebroadcasting it until it expires.
type Broadcast struct {
	Result    *chain.BroadcastResult
	Err       error
	FailCode  string
	Permanent bool
}

// Hooks are the caller's bookkeeping between the on-chain steps. Every hook may
// return ErrStop to end the transfer without an error, or any other error to
// abort with it.
type Hooks struct {
	// Energy makes the estimated energy available. It runs before anything is
	// built or signed, which is the only point where a transfer can still be
	// abandoned for free.
	Energy func(ctx context.Context, need int64) error
	// Signed persists the signed bytes before they reach a node, so a transfer
	// that is broadcast is never missing from the caller's table.
	Signed func(ctx context.Context, signed *Signed) error
	// Broadcast records the outcome. Returning nil keeps Send's own error, which
	// is the broadcast error, so a caller that only stores the outcome does not
	// have to repeat it.
	Broadcast func(ctx context.Context, out *Broadcast) error
	// Fail settles a transfer that failed before it was broadcast, with the
	// chain fail code of the step that failed.
	Fail func(ctx context.Context, failCode string, cause error) error
}

// Send runs the whole on-chain sequence for one transfer. The returned Signed
// is nil until the transaction was signed, so a caller can tell a transfer that
// may be on chain from one that certainly is not.
func (e *Executor) Send(ctx context.Context, req Request, h Hooks) (*Signed, error) {
	need := e.Estimate(ctx, req)
	if h.Energy != nil {
		if err := h.Energy(ctx, need); err != nil {
			return nil, err
		}
	}
	// Only now does a transaction with an expiration exist.
	tx, err := e.gw.BuildTRC20Transfer(ctx, req.From, req.Contract, req.Data, req.FeeLimitSun)
	if err != nil {
		return nil, e.fail(ctx, h, chain.FailValidate, err)
	}
	response, err := e.sign.Sign(ctx, &signer.SignRequest{
		Purpose: req.Purpose,
		Path:    req.DerivePath,
		Address: req.From,
		Tx:      tx,
		Meta:    e.meta(req),
	})
	if err != nil {
		return nil, e.fail(ctx, h, chain.FailSignature, err)
	}
	signed := &Signed{
		TxID:       response.TxID,
		RawDataHex: response.Tx.RawDataHex,
		Energy:     need,
		tx:         response.Tx,
	}
	if req.ExpirationSec > 0 {
		signed.ExpiredAt = time.Now().Add(time.Duration(req.ExpirationSec) * time.Second)
	}
	if h.Signed != nil {
		if err := h.Signed(ctx, signed); err != nil {
			return signed, err
		}
	}
	out := e.broadcast(ctx, signed)
	if h.Broadcast != nil {
		if err := h.Broadcast(ctx, out); err != nil {
			return signed, err
		}
	}
	return signed, out.Err
}

func (e *Executor) broadcast(ctx context.Context, signed *Signed) *Broadcast {
	result, err := e.gw.Broadcast(ctx, signed.tx)
	out := &Broadcast{Result: result, Err: err}
	if err != nil {
		out.FailCode, out.Permanent = chain.FailNode, false
		if result != nil {
			out.FailCode, out.Permanent = chain.ClassifyBroadcast(result.Code, result.Message)
		}
	}
	return out
}

func (e *Executor) fail(ctx context.Context, h Hooks, failCode string, cause error) error {
	if h.Fail != nil {
		if err := h.Fail(ctx, failCode, cause); err != nil {
			return err
		}
	}
	return cause
}

func (e *Executor) meta(req Request) signer.SignMeta {
	units := ""
	if req.Amount != nil {
		units = req.Amount.String()
	}
	return signer.SignMeta{ToAddress: req.To, Contract: req.Contract, AmountUnits: units}
}

// Estimate is how much energy the transfer needs. The read-only call it uses
// carries no expiration and does not require the address to hold any energy, so
// it can safely run before the energy is there; when it fails the configured
// worst case is used instead of giving up on the transfer.
func (e *Executor) Estimate(ctx context.Context, req Request) int64 {
	need, err := e.mgr.EstimateEnergyFactor(ctx, req.From, req.Contract, req.Data, req.EnergyFactor)
	if err == nil {
		return need
	}
	logrus.Warn("transfer energy estimate failed, using the configured worst case",
		",purpose:", req.Purpose, ",address:", req.From, ",fallback_energy:", req.FallbackEnergy, ",err:", err)
	return req.FallbackEnergy
}

// RebroadcastRequest resends bytes that were already signed once.
type RebroadcastRequest struct {
	Purpose     string
	From        string
	DerivePath  string
	To          string
	Contract    string
	AmountUnits string
	TxID        string
	SignedRaw   string
}

// Rebroadcast re-signs the stored raw data - deterministic for the same key and
// payload - and sends the identical bytes again. It is the only safe retry for a
// signed transfer that is not on chain: building a second transaction while the
// first one may still be included would send the amount twice. A txid that
// changed means the bytes are not the same transfer any more, so nothing is
// broadcast.
func (e *Executor) Rebroadcast(ctx context.Context, req RebroadcastRequest) (*chain.BroadcastResult, error) {
	signed, err := e.sign.Sign(ctx, &signer.SignRequest{
		Purpose: req.Purpose,
		Path:    req.DerivePath,
		Address: req.From,
		Tx:      &tron.Transaction{TxID: req.TxID, RawDataHex: req.SignedRaw},
		Meta: signer.SignMeta{
			ToAddress:   req.To,
			Contract:    req.Contract,
			AmountUnits: req.AmountUnits,
		},
	})
	if err != nil {
		return nil, err
	}
	if signed.TxID != req.TxID {
		return nil, fmt.Errorf("transfer: refusing to rebroadcast, txid changed from %s to %s",
			req.TxID, signed.TxID)
	}
	return e.gw.Broadcast(ctx, signed.Tx)
}

// TokenBalance is the TRC20 balance of an address, in minimum units.
func (e *Executor) TokenBalance(ctx context.Context, contract, address string) (*big.Int, error) {
	data, err := tron.EncodeTRC20BalanceOf(address)
	if err != nil {
		return nil, err
	}
	out, _, err := e.gw.TriggerConstantContract(ctx, address, contract, data)
	if err != nil {
		return nil, err
	}
	value, ok := tron.ParseUint256(out)
	if !ok {
		return nil, fmt.Errorf("transfer: cannot parse balance %q", out)
	}
	return value, nil
}

// BurnBudget is what a transfer paying its own fee costs the signing address and
// what that address holds, both in sun.
type BurnBudget struct {
	CostSun    int64
	BalanceSun int64
}

// Enough reports whether the address can pay the fee itself.
func (b BurnBudget) Enough() bool { return b.BalanceSun >= b.CostSun }

// BurnBudget prices the energy and bandwidth the address would have to buy with
// its own TRX, which is what energy.rental_enabled=false asks of it. Only the
// missing resources are billed, so an address with a delegation left over is not
// asked for TRX it does not need.
func (e *Executor) BurnBudget(ctx context.Context, address string, need int64) (BurnBudget, error) {
	params, err := e.gw.GetChainParameters(ctx)
	if err != nil {
		return BurnBudget{}, fmt.Errorf("transfer: chain parameters query failed: %w", err)
	}
	res, err := e.gw.GetAccountResource(ctx, address)
	if err != nil {
		return BurnBudget{}, fmt.Errorf("transfer: account resource query failed: %w", err)
	}
	balance, err := e.gw.GetTRXBalance(ctx, address)
	if err != nil {
		return BurnBudget{}, fmt.Errorf("transfer: TRX balance query failed: %w", err)
	}
	return BurnBudget{CostSun: energy.BurnCostSun(res, params, need), BalanceSun: balance}, nil
}

// AmountText renders a minimum-unit amount as a human readable token amount
// (1000000 with 6 decimals -> "1"), so an operator reading a log sees the same
// number the merchant asked for instead of counting zeros.
func AmountText(units string, decimals int) string {
	value, ok := new(big.Int).SetString(units, 10)
	if !ok || decimals < 0 {
		return units
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole, frac := new(big.Int).QuoRem(value, scale, new(big.Int))
	if decimals == 0 || frac.Sign() == 0 {
		return whole.String()
	}
	digits := strings.TrimRight(fmt.Sprintf("%0*s", decimals, frac.String()), "0")
	return whole.String() + "." + digits
}

// Truncate caps a reason at the width of the fail_reason column.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
