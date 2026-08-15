package tron

import (
	"math/big"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
)

func TestEncodeTRC20Transfer(t *testing.T) {
	amount := big.NewInt(1500000) // 1.5 USDT at 6 decimals
	data, err := EncodeTRC20Transfer(usdtAddress, amount)
	if err != nil {
		t.Fatalf("EncodeTRC20Transfer: %v", err)
	}
	if len(data) != 8+64+64 {
		t.Fatalf("calldata length = %d, want %d", len(data), 8+64+64)
	}
	if !strings.HasPrefix(data, "a9059cbb") {
		t.Fatalf("selector = %s, want a9059cbb", data[:8])
	}
	// The address is right aligned in a 32 byte word without the 0x41 prefix.
	wantAddr := strings.ToLower(usdtHex[2:])
	if !strings.Contains(strings.ToLower(data), wantAddr) {
		t.Fatalf("calldata %s does not contain the destination %s", data, wantAddr)
	}
	if !strings.HasSuffix(data, "16e360") {
		t.Fatalf("amount encoding wrong, got tail %s", data[len(data)-6:])
	}
}

func TestEncodeTRC20TransferRejectsBadAddress(t *testing.T) {
	if _, err := EncodeTRC20Transfer("not-an-address", big.NewInt(1)); err == nil {
		t.Fatal("expected an error for an invalid destination")
	}
}

func TestParseUint256(t *testing.T) {
	word := strings.Repeat("0", 58) + "0016e360"
	got, ok := ParseUint256(word)
	if !ok {
		t.Fatal("ParseUint256 failed")
	}
	if got.Int64() != 1500000 {
		t.Fatalf("got %s, want 1500000", got)
	}
	if _, ok := ParseUint256("zz"); ok {
		t.Fatal("expected failure on non-hex input")
	}
}

func TestSignTransactionRejectsTxIDMismatch(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	// A node reported txid that disagrees with the raw data means the payload
	// was altered in flight; signing it would sign someone else's transfer.
	tx := &Transaction{
		TxID:       strings.Repeat("ab", 32),
		RawDataHex: "0a02" + strings.Repeat("00", 20),
	}
	if _, err := SignTransaction(tx, priv); err == nil {
		t.Fatal("expected a txid mismatch error")
	}
}

// The serialized form is what a rebroadcast sends, so the field framing and the
// varint length of a realistic (>127 byte) raw_data must be exact.
func TestSerializeSignedFramesRawDataAndSignature(t *testing.T) {
	raw := strings.Repeat("aa", 200)
	sig := strings.Repeat("bb", 65)
	got, err := SerializeSigned(&Transaction{RawDataHex: raw, Signature: []string{sig}})
	if err != nil {
		t.Fatalf("SerializeSigned: %v", err)
	}
	// field 1 (0a) length 200 as varint c801, then field 2 (12) length 65 (41).
	want := "0ac801" + raw + "1241" + sig
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSerializeSignedRejectsUnsignedTransaction(t *testing.T) {
	if _, err := SerializeSigned(&Transaction{RawDataHex: "0a02aabb"}); err == nil {
		t.Fatal("expected an error for a transaction without a signature")
	}
	if _, err := SerializeSigned(&Transaction{Signature: []string{"aa"}}); err == nil {
		t.Fatal("expected an error for a transaction without raw_data_hex")
	}
}

func TestSignTransactionProducesRecoverableSignature(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := "0a02" + strings.Repeat("00", 20)
	txid, err := TxIDFromRawHex(raw)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignTransaction(&Transaction{RawDataHex: raw}, priv)
	if err != nil {
		t.Fatalf("SignTransaction: %v", err)
	}
	if signed.TxID != txid {
		t.Fatalf("txid = %s, want %s", signed.TxID, txid)
	}
	if len(signed.Signature) != 1 || len(signed.Signature[0]) != 130 {
		t.Fatalf("signature = %v, want one 65 byte hex string", signed.Signature)
	}
	// The recovery id must be normalised to 0/1, not btcec's 27/28.
	v := signed.Signature[0][128:]
	if v != "00" && v != "01" {
		t.Fatalf("recovery id = %s, want 00 or 01", v)
	}
}
