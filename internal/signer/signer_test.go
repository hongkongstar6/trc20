package signer

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/tron"
)

const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

const (
	usdtContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	financeAddr  = "TZJ9qkoxUB1SGdbtChgAjUphBmkJwAeBaW"
	depositAddr  = "TSbUSxRQC7i41NJBnD22pDcFRVWST4q6bX"
	outsideAddr  = "TPP34NBQ2iUNFgnF1GeReqmdytP8rE8RyA"
)

func newTestService(t *testing.T, policy Policy) *Service {
	t.Helper()
	svc, err := New(config.SignConfig{Mnemonic: testMnemonic}, policy, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// transferTx builds a request whose raw body genuinely contains the declared
// transfer calldata, which is what the policy checks against.
func transferTx(t *testing.T, to string, amount *big.Int) *tron.Transaction {
	t.Helper()
	data, err := tron.EncodeTRC20Transfer(to, amount)
	if err != nil {
		t.Fatal(err)
	}
	return &tron.Transaction{RawDataHex: "0a02aaaa" + data}
}

func TestSweepRejectsForeignDestination(t *testing.T) {
	svc := newTestService(t, Policy{
		SweepDestination: financeAddr,
		AllowedContracts: map[string]bool{usdtContract: true},
	})
	amount := big.NewInt(1000000)
	req := &SignRequest{
		Purpose: PurposeSweep,
		Path:    "m/44'/195'/0'/0/0",
		Tx:      transferTx(t, outsideAddr, amount),
		Meta:    SignMeta{ToAddress: outsideAddr, Contract: usdtContract, AmountUnits: amount.String()},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("a sweep to a non finance address must be refused")
	}
}

func TestSweepRejectsUnknownContract(t *testing.T) {
	svc := newTestService(t, Policy{
		SweepDestination: financeAddr,
		AllowedContracts: map[string]bool{usdtContract: true},
	})
	amount := big.NewInt(1000000)
	req := &SignRequest{
		Purpose: PurposeSweep,
		Path:    "m/44'/195'/0'/0/0",
		Tx:      transferTx(t, financeAddr, amount),
		Meta:    SignMeta{ToAddress: financeAddr, Contract: outsideAddr, AmountUnits: amount.String()},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("an unlisted token contract must be refused")
	}
}

// The declared intent and the bytes that will execute must agree, otherwise a
// compromised worker could declare a small transfer and sign a large one.
func TestSignRejectsCalldataMismatch(t *testing.T) {
	svc := newTestService(t, Policy{
		SweepDestination: financeAddr,
		AllowedContracts: map[string]bool{usdtContract: true},
	})
	declared := big.NewInt(1000000)
	actual := big.NewInt(999000000)
	req := &SignRequest{
		Purpose: PurposeSweep,
		Path:    "m/44'/195'/0'/0/0",
		Tx:      transferTx(t, financeAddr, actual),
		Meta:    SignMeta{ToAddress: financeAddr, Contract: usdtContract, AmountUnits: declared.String()},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("the declared amount does not match the calldata, signing must be refused")
	}
}

func TestWithdrawMustOriginateFromHotWallet(t *testing.T) {
	svc := newTestService(t, Policy{
		WithdrawFrom:     financeAddr,
		AllowedContracts: map[string]bool{usdtContract: true},
	})
	amount := big.NewInt(1000000)
	req := &SignRequest{
		Purpose: PurposeWithdraw,
		Path:    "m/44'/195'/0'/0/0",
		Address: outsideAddr,
		Tx:      transferTx(t, outsideAddr, amount),
		Meta:    SignMeta{ToAddress: outsideAddr, Contract: usdtContract, AmountUnits: amount.String()},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("withdrawals from a non hot wallet address must be refused")
	}
}

func topupPolicy() Policy {
	return Policy{
		GasAccountAddress: financeAddr,
		TopupWhitelist:    map[string]string{"gasstation": depositAddr},
		TopupMaxSun:       4000 * 1e6,
	}
}

func topupTx(t *testing.T, to string) *tron.Transaction {
	t.Helper()
	hexAddr, err := tron.AddressToHex(to)
	if err != nil {
		t.Fatal(err)
	}
	return &tron.Transaction{RawDataHex: "0a02bbbb" + hexAddr}
}

func TestTopupRejectsNonWhitelistedDestination(t *testing.T) {
	svc := newTestService(t, topupPolicy())
	req := &SignRequest{
		Purpose: PurposeTopup,
		Path:    "m/44'/195'/3'/0/0",
		Address: financeAddr,
		Tx:      topupTx(t, outsideAddr),
		Meta:    SignMeta{ToAddress: outsideAddr, AmountSun: 100 * 1e6},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("a topup to an address outside the whitelist must be refused")
	}
}

func TestTopupRejectsAmountAboveCap(t *testing.T) {
	svc := newTestService(t, topupPolicy())
	req := &SignRequest{
		Purpose: PurposeTopup,
		Path:    "m/44'/195'/3'/0/0",
		Address: financeAddr,
		Tx:      topupTx(t, depositAddr),
		Meta:    SignMeta{ToAddress: depositAddr, AmountSun: 9000 * 1e6},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("a topup above the per transfer cap must be refused")
	}
}

func TestTopupRejectsTokenTransfer(t *testing.T) {
	svc := newTestService(t, topupPolicy())
	req := &SignRequest{
		Purpose: PurposeTopup,
		Path:    "m/44'/195'/3'/0/0",
		Address: financeAddr,
		Tx:      topupTx(t, depositAddr),
		Meta:    SignMeta{ToAddress: depositAddr, AmountSun: 100 * 1e6, Contract: usdtContract},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("a topup carrying a token contract must be refused")
	}
}

func TestTopupRejectsNonGasAccountSource(t *testing.T) {
	svc := newTestService(t, topupPolicy())
	req := &SignRequest{
		Purpose: PurposeTopup,
		Path:    "m/44'/195'/2'/0/0",
		Address: outsideAddr,
		Tx:      topupTx(t, depositAddr),
		Meta:    SignMeta{ToAddress: depositAddr, AmountSun: 100 * 1e6},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("only the gas account may fund providers")
	}
}

func TestUnknownPurposeIsRefused(t *testing.T) {
	svc := newTestService(t, Policy{})
	req := &SignRequest{
		Purpose: "anything",
		Path:    "m/44'/195'/0'/0/0",
		Tx:      &tron.Transaction{RawDataHex: "0a02"},
	}
	if _, err := svc.Sign(context.Background(), req, "test"); err == nil {
		t.Fatal("an unknown purpose must be refused")
	}
}

// A path/address mismatch means the caller is confused about which key it is
// asking for; signing anyway would move funds from an unexpected address.
func TestSignRejectsPathAddressMismatch(t *testing.T) {
	svc := newTestService(t, Policy{
		SweepDestination: financeAddr,
		AllowedContracts: map[string]bool{usdtContract: true},
	})
	amount := big.NewInt(1000000)
	req := &SignRequest{
		Purpose: PurposeSweep,
		Path:    "m/44'/195'/0'/0/0",
		Address: outsideAddr,
		Tx:      transferTx(t, financeAddr, amount),
		Meta:    SignMeta{ToAddress: financeAddr, Contract: usdtContract, AmountUnits: amount.String()},
	}
	_, err := svc.Sign(context.Background(), req, "test")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected an address mismatch error, got %v", err)
	}
}

func TestSweepSignsValidRequest(t *testing.T) {
	svc := newTestService(t, Policy{
		SweepDestination: financeAddr,
		AllowedContracts: map[string]bool{usdtContract: true},
	})
	const path = "m/44'/195'/0'/0/5"
	addr, err := svc.DeriveAddress(path)
	if err != nil {
		t.Fatal(err)
	}
	amount := big.NewInt(120000000)
	req := &SignRequest{
		Purpose: PurposeSweep,
		Path:    path,
		Address: addr,
		Tx:      transferTx(t, financeAddr, amount),
		Meta:    SignMeta{ToAddress: financeAddr, Contract: usdtContract, AmountUnits: amount.String()},
	}
	resp, err := svc.Sign(context.Background(), req, "test")
	if err != nil {
		t.Fatalf("a compliant sweep must be signed: %v", err)
	}
	if resp.TxID == "" || len(resp.Tx.Signature) != 1 {
		t.Fatalf("unexpected response %+v", resp)
	}
}

func TestPolicyFromConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Wallet.HotWallet.Address = outsideAddr
	cfg.Wallet.FinanceWallet.Address = financeAddr
	cfg.Wallet.Tokens = []config.TokenConfig{
		{Symbol: "USDT", Contract: usdtContract, Enabled: true},
		{Symbol: "OTHER", Contract: outsideAddr, Enabled: false},
	}
	cfg.Energy.AutoTopup.SourceAddress = financeAddr
	cfg.Energy.AutoTopup.Providers = map[string]config.ProviderTopupConf{
		"gasstation":     {DepositAddressUrl: depositAddr, MaxSingleTopupTRX: 4000},
		"tronenergyrent": {DepositAddressUrl: outsideAddr, MaxSingleTopupTRX: 1000},
	}
	config.Cfg = cfg
	policy := PolicyFromConfig()

	if policy.WithdrawFrom != outsideAddr || policy.SweepDestination != financeAddr {
		t.Fatalf("named addresses not carried over: %+v", policy)
	}
	if !policy.AllowedContracts[usdtContract] || policy.AllowedContracts[outsideAddr] {
		t.Fatalf("only enabled tokens may be allowed: %+v", policy.AllowedContracts)
	}
	if policy.TopupWhitelist["gasstation"] != depositAddr {
		t.Fatalf("whitelist not built: %+v", policy.TopupWhitelist)
	}
	// The cap is the widest configured per transfer limit.
	if policy.TopupMaxSun != 4000*1e6 {
		t.Fatalf("TopupMaxSun = %d, want %d", policy.TopupMaxSun, int64(4000*1e6))
	}
}
