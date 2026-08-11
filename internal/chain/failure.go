package chain

import "strings"

// Stable failure codes stored on sweep_record.fail_code and
// withdraw_record.fail_code. The human readable node message keeps going to
// fail_reason; only these codes are branched on.
const (
	FailOutOfEnergy  = "out_of_energy"
	FailOutOfTime    = "out_of_time"
	FailRevert       = "revert"
	FailContractExec = "contract_exec"

	FailSignature = "sig_error"
	FailValidate  = "validate_error"
	FailBandwidth = "bandwidth"
	FailTapos     = "tapos_error"
	FailTooBig    = "too_big_tx"
	FailExpired   = "expired"
	FailNode      = "node_error"
	FailUnknown   = "unknown"
)

// ClassifyReceipt maps an on-chain receipt of a failed transaction onto a
// failure code. Only OUT_OF_ENERGY is worth retrying with more energy; the
// contract level failures repeat identically no matter how much is rented.
func ClassifyReceipt(info *TxInfo) string {
	if info == nil {
		return FailUnknown
	}
	switch strings.ToUpper(strings.TrimSpace(info.Receipt.Result)) {
	case "OUT_OF_ENERGY":
		return FailOutOfEnergy
	case "OUT_OF_TIME":
		return FailOutOfTime
	case "REVERT":
		return FailRevert
	case "BAD_JUMP_DESTINATION", "ILLEGAL_OPERATION", "STACK_TOO_SMALL",
		"STACK_TOO_LARGE", "TRANSFER_FAILED", "INVALID_CODE", "JVM_STACK_OVER_FLOW",
		"OUT_OF_MEMORY", "PRECOMPILED_CONTRACT", "STACK_OVERFLOW", "UNKNOWN":
		return FailContractExec
	}
	// A receipt without a result still carries the reason in resMessage.
	msg := strings.ToLower(info.ResMessage)
	switch {
	case strings.Contains(msg, "out of energy"),
		strings.Contains(msg, "not enough energy"),
		strings.Contains(msg, "energy limit"):
		return FailOutOfEnergy
	case strings.Contains(msg, "revert"):
		return FailRevert
	case msg != "":
		return FailContractExec
	}
	return FailUnknown
}

// ClassifyBroadcast maps a node rejection onto a failure code and reports
// whether rebroadcasting the identical bytes can ever succeed. A permanent
// rejection must fail the order right away instead of being rebroadcast every
// reconcile round until the transaction expires.
func ClassifyBroadcast(code, message string) (failCode string, permanent bool) {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "SIGERROR":
		return FailSignature, true
	case "CONTRACT_VALIDATE_ERROR":
		return FailValidate, true
	case "CONTRACT_EXE_ERROR":
		return FailContractExec, true
	case "BANDWITH_ERROR", "BANDWIDTH_ERROR":
		return FailBandwidth, true
	case "TAPOS_ERROR":
		return FailTapos, true
	case "TOO_BIG_TRANSACTION_ERROR":
		return FailTooBig, true
	case "TRANSACTION_EXPIRATION_ERROR", "EXPIRATION_ERROR":
		return FailExpired, true
	case "SERVER_BUSY", "NO_CONNECTION", "NOT_ENOUGH_EFFECTIVE_CONNECTION", "BLOCK_UNSOLIDIFIED", "OTHER_ERROR":
		return FailNode, false
	}
	// Some nodes answer with a bare message and no code.
	msg := strings.ToLower(message)
	switch {
	case strings.Contains(msg, "signature"):
		return FailSignature, true
	case strings.Contains(msg, "expired"):
		return FailExpired, true
	case strings.Contains(msg, "balance is not sufficient"),
		strings.Contains(msg, "assert null"),
		strings.Contains(msg, "validate"):
		return FailValidate, true
	case strings.Contains(msg, "bandwidth"):
		return FailBandwidth, true
	}
	// Unknown reasons are treated as transient: the reconciler still settles
	// them once the transaction expires without inclusion.
	return FailNode, false
}
