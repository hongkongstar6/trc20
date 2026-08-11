package chain

import "testing"

func TestClassifyReceiptSeparatesEnergyFromContractFailures(t *testing.T) {
	cases := []struct {
		name       string
		result     string
		receipt    string
		resMessage string
		want       string
	}{
		{name: "out of energy", receipt: "OUT_OF_ENERGY", want: FailOutOfEnergy},
		{name: "revert", receipt: "REVERT", want: FailRevert},
		{name: "out of time", receipt: "OUT_OF_TIME", want: FailOutOfTime},
		{name: "transfer failed", receipt: "TRANSFER_FAILED", want: FailContractExec},
		{name: "message only", result: "FAILED", resMessage: "Not enough energy for 'SWAP1' operation", want: FailOutOfEnergy},
		{name: "nothing", want: FailUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var info TxInfo
			info.Result = c.result
			info.Receipt.Result = c.receipt
			info.ResMessage = c.resMessage
			if got := ClassifyReceipt(&info); got != c.want {
				t.Fatalf("ClassifyReceipt = %q, want %q", got, c.want)
			}
		})
	}
}

// A permanent rejection must not be rebroadcast: the identical bytes can never
// become valid, so the order has to fail right away.
func TestClassifyBroadcastMarksUnretryableRejectionsPermanent(t *testing.T) {
	cases := []struct {
		code      string
		message   string
		want      string
		permanent bool
	}{
		{code: "SIGERROR", want: FailSignature, permanent: true},
		{code: "CONTRACT_VALIDATE_ERROR", message: "balance is not sufficient", want: FailValidate, permanent: true},
		{code: "TAPOS_ERROR", want: FailTapos, permanent: true},
		{code: "BANDWITH_ERROR", want: FailBandwidth, permanent: true},
		{code: "SERVER_BUSY", want: FailNode, permanent: false},
		{code: "", message: "connection reset by peer", want: FailNode, permanent: false},
		{code: "", message: "Transaction expired", want: FailExpired, permanent: true},
	}
	for _, c := range cases {
		got, permanent := ClassifyBroadcast(c.code, c.message)
		if got != c.want || permanent != c.permanent {
			t.Fatalf("ClassifyBroadcast(%q, %q) = (%q, %v), want (%q, %v)",
				c.code, c.message, got, permanent, c.want, c.permanent)
		}
	}
}
