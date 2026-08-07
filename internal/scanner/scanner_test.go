package scanner

import (
	"testing"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/tron"
)

const (
	usdtBase58 = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	usdtHexLog = "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	// Topic addresses are 32 byte words: 12 zero bytes then the 20 byte body.
	fromTopic = "000000000000000000000000a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	toTopic   = "0000000000000000000000004115208eff988924a8ba9b7b0e2b6a3a02c0e0e1"
	// 1.5 USDT at 6 decimals.
	amountData = "000000000000000000000000000000000000000000000000000000000016e360"
)

func newTestScanner(t *testing.T, minDeposit string) *Scanner {
	t.Helper()
	cfg := &config.Config{}
	cfg.Wallet.Tokens = []config.TokenConfig{
		{Symbol: "USDT", Contract: usdtBase58, Decimals: 6, Enabled: true},
	}
	cfg.Deposit.MinDepositUnits = minDeposit
	return New(nil, nil)
}

func validLog() chain.TxLog {
	return chain.TxLog{
		Address: usdtHexLog,
		Topics:  []string{tron.TransferEventTopic, fromTopic, toTopic},
		Data:    amountData,
	}
}

func TestDecodeTransferAcceptsValidLog(t *testing.T) {
	s := newTestScanner(t, "0")
	got, ok := s.decodeTransfer(validLog())
	if !ok {
		t.Fatal("a well formed USDT Transfer log must be decoded")
	}
	if got.contract != usdtBase58 {
		t.Fatalf("contract = %s", got.contract)
	}
	if got.amount.String() != "1500000" {
		t.Fatalf("amount = %s, want 1500000", got.amount)
	}
	if got.token.symbol != "USDT" || got.token.decimals != 6 {
		t.Fatalf("token = %+v", got.token)
	}
	if !tron.IsValidAddress(got.to) || !tron.IsValidAddress(got.from) {
		t.Fatalf("addresses not decoded: from=%s to=%s", got.from, got.to)
	}
}

// Every one of these is a way to fake a deposit, so each must be rejected
// before any database lookup happens.
func TestDecodeTransferRejectsFakeDeposits(t *testing.T) {
	s := newTestScanner(t, "0")

	cases := []struct {
		name   string
		mutate func(*chain.TxLog)
	}{
		{"another event of the same contract", func(l *chain.TxLog) {
			// Approval has the same 3 topic shape as Transfer.
			l.Topics[0] = "8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"
		}},
		{"non allowlisted contract", func(l *chain.TxLog) {
			l.Address = "4115208eff988924a8ba9b7b0e2b6a3a02c0e0e100"
		}},
		{"unindexed transfer with two topics", func(l *chain.TxLog) {
			l.Topics = l.Topics[:2]
		}},
		{"zero amount", func(l *chain.TxLog) {
			l.Data = "0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{"non hex amount", func(l *chain.TxLog) {
			l.Data = "not-a-number"
		}},
		{"malformed recipient", func(l *chain.TxLog) {
			l.Topics[2] = "zz"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lg := validLog()
			c.mutate(&lg)
			if _, ok := s.decodeTransfer(lg); ok {
				t.Fatalf("%s must not produce a deposit", c.name)
			}
		})
	}
}

func TestDecodeTransferAppliesDustFilter(t *testing.T) {
	s := newTestScanner(t, "2000000") // 2 USDT
	if _, ok := s.decodeTransfer(validLog()); ok {
		t.Fatal("1.5 USDT is below the 2 USDT dust threshold and must be dropped")
	}
	s = newTestScanner(t, "1000000") // 1 USDT
	if _, ok := s.decodeTransfer(validLog()); !ok {
		t.Fatal("1.5 USDT is above the 1 USDT threshold and must be kept")
	}
}

func TestNewIgnoresDisabledTokens(t *testing.T) {
	cfg := &config.Config{}
	cfg.Wallet.Tokens = []config.TokenConfig{
		{Symbol: "USDT", Contract: usdtBase58, Decimals: 6, Enabled: false},
	}
	s := New(nil, nil)
	if _, ok := s.decodeTransfer(validLog()); ok {
		t.Fatal("a disabled token must not be scanned")
	}
}

func TestDepositEventIDIsStable(t *testing.T) {
	rec := depositRecordFixture()
	if got := depositEventID(rec); got != "abc123:2" {
		t.Fatalf("event id = %s, want abc123:2", got)
	}
	// The id must depend on the event index, not just the transaction: one
	// transaction can carry several transfers to different users.
	rec.EventIndex = 3
	if got := depositEventID(rec); got != "abc123:3" {
		t.Fatalf("event id = %s, want abc123:3", got)
	}
}
