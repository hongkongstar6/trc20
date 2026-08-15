package withdraw

import (
	"testing"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/tron"
)

const (
	// USDT mainnet contract, as the 41 prefixed hex a log carries.
	usdtHexLog = "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	// Topic addresses are 32 byte words: 12 zero bytes then the 20 byte body.
	hotTopic  = "000000000000000000000000a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	userTopic = "0000000000000000000000004115208eff988924a8ba9b7b0e2b6a3a02c0e0e1"
	// 1.5 USDT at 6 decimals.
	amountData = "000000000000000000000000000000000000000000000000000000000016e360"
)

func addr(t *testing.T, hexAddr string) string {
	t.Helper()
	a, err := tron.HexToAddress(hexAddr)
	if err != nil {
		t.Fatalf("HexToAddress(%s): %v", hexAddr, err)
	}
	return a
}

func testRow(t *testing.T) model.WithdrawRecord {
	t.Helper()
	return model.WithdrawRecord{
		OrderNo:     "order-1",
		Contract:    addr(t, usdtHexLog),
		FromAddress: addr(t, hotTopic),
		ToAddress:   addr(t, userTopic),
		AmountUnits: "1500000",
	}
}

func transferLog() chain.TxLog {
	return chain.TxLog{
		Address: usdtHexLog,
		Topics:  []string{tron.TransferEventTopic, hotTopic, userTopic},
		Data:    amountData,
	}
}

func TestTransferredAcceptsMatchingTransferEvent(t *testing.T) {
	info := &chain.TxInfo{Log: []chain.TxLog{transferLog()}}
	if !transferred(info, testRow(t)) {
		t.Fatal("the receipt of the order's own transfer must settle as confirmed")
	}
}

// Each of these is a receipt that reports success while the order's tokens
// never reached the user, so none of them may settle as confirmed.
func TestTransferredRejectsReceiptWithoutTheOrdersTransfer(t *testing.T) {
	otherTopic := "000000000000000000000000415208eff988924a8ba9b7b0e2b6a3a02c0e0e11"
	cases := []struct {
		name string
		logs []chain.TxLog
	}{
		{"no event at all", nil},
		{"unrelated event", []chain.TxLog{{Address: usdtHexLog, Topics: []string{"deadbeef", hotTopic, userTopic}, Data: amountData}}},
		{"another token", []chain.TxLog{{Address: "41b614f803b6fd780986a42c78ec9c7f77e6ded13c", Topics: []string{tron.TransferEventTopic, hotTopic, userTopic}, Data: amountData}}},
		{"another recipient", []chain.TxLog{{Address: usdtHexLog, Topics: []string{tron.TransferEventTopic, hotTopic, otherTopic}, Data: amountData}}},
		{"another sender", []chain.TxLog{{Address: usdtHexLog, Topics: []string{tron.TransferEventTopic, otherTopic, userTopic}, Data: amountData}}},
		{"short amount", []chain.TxLog{{Address: usdtHexLog, Topics: []string{tron.TransferEventTopic, hotTopic, userTopic}, Data: "0000000000000000000000000000000000000000000000000000000000000001"}}},
		{"non indexed transfer", []chain.TxLog{{Address: usdtHexLog, Topics: []string{tron.TransferEventTopic}, Data: amountData}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if transferred(&chain.TxInfo{Log: c.logs}, testRow(t)) {
				t.Fatal("a receipt without the order's own Transfer event must not confirm the order")
			}
		})
	}
}
