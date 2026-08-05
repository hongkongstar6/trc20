package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// Transaction mirrors the JSON transaction returned by the node HTTP API.
// The node builds raw_data for us, so no protobuf dependency is needed; we
// only sign the sha256 of raw_data_hex and attach the signature.
type Transaction struct {
	Visible    bool           `json:"visible"`
	TxID       string         `json:"txID"`
	RawData    map[string]any `json:"raw_data,omitempty"`
	RawDataHex string         `json:"raw_data_hex"`
	Signature  []string       `json:"signature,omitempty"`
}

// TxIDFromRawHex recomputes the transaction id from raw_data_hex. It is used
// to verify that the node returned a transaction we actually asked for.
func TxIDFromRawHex(rawHex string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(rawHex, "0x"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// SignTransaction signs raw_data_hex with priv and returns a copy carrying the
// 65-byte [R||S||V] signature TRON expects.
func SignTransaction(tx *Transaction, priv *btcec.PrivateKey) (*Transaction, error) {
	if tx == nil || tx.RawDataHex == "" {
		return nil, errors.New("transaction has no raw_data_hex")
	}
	txid, err := TxIDFromRawHex(tx.RawDataHex)
	if err != nil {
		return nil, err
	}
	if tx.TxID != "" && !strings.EqualFold(tx.TxID, txid) {
		return nil, fmt.Errorf("txid mismatch: node=%s computed=%s", tx.TxID, txid)
	}
	digest, err := hex.DecodeString(txid)
	if err != nil {
		return nil, err
	}
	sig, err := signRecoverable(digest, priv)
	if err != nil {
		return nil, err
	}
	signed := *tx
	signed.TxID = txid
	signed.Signature = []string{hex.EncodeToString(sig)}
	return &signed, nil
}

// signRecoverable produces the 65-byte signature layout used by TRON:
// R (32) || S (32) || V (1, recovery id 0/1).
func signRecoverable(digest []byte, priv *btcec.PrivateKey) ([]byte, error) {
	compact := ecdsa.SignCompact(priv, digest, false)
	if len(compact) != 65 {
		return nil, fmt.Errorf("unexpected signature length %d", len(compact))
	}
	// btcec returns [V||R||S] with V = 27 + recid (+4 when compressed).
	v := compact[0] - 27
	if v >= 4 {
		v -= 4
	}
	out := make([]byte, 65)
	copy(out[:64], compact[1:])
	out[64] = v
	return out, nil
}

// EncodeTRC20Transfer builds the calldata for transfer(address,uint256).
func EncodeTRC20Transfer(to string, amount *big.Int) (string, error) {
	raw, err := DecodeAddress(to)
	if err != nil {
		return "", fmt.Errorf("encode transfer: %w", err)
	}
	selector := Keccak256([]byte("transfer(address,uint256)"))[:4]
	param := make([]byte, 64)
	copy(param[12:32], raw[1:]) // 20-byte address, left padded
	amtBytes := amount.Bytes()
	if len(amtBytes) > 32 {
		return "", errors.New("amount overflows uint256")
	}
	copy(param[64-len(amtBytes):], amtBytes)
	return hex.EncodeToString(append(selector, param...)), nil
}

// EncodeTRC20BalanceOf builds the calldata for balanceOf(address).
func EncodeTRC20BalanceOf(owner string) (string, error) {
	raw, err := DecodeAddress(owner)
	if err != nil {
		return "", fmt.Errorf("encode balanceOf: %w", err)
	}
	selector := Keccak256([]byte("balanceOf(address)"))[:4]
	param := make([]byte, 32)
	copy(param[12:32], raw[1:])
	return hex.EncodeToString(append(selector, param...)), nil
}

// TransferEventTopic is keccak256("Transfer(address,address,uint256)").
var TransferEventTopic = hex.EncodeToString(Keccak256([]byte("Transfer(address,address,uint256)")))

// ParseUint256 decodes a 32-byte hex word into a big.Int.
func ParseUint256(word string) (*big.Int, bool) {
	word = strings.TrimPrefix(strings.ToLower(word), "0x")
	b, err := hex.DecodeString(word)
	if err != nil {
		return nil, false
	}
	return new(big.Int).SetBytes(b), true
}
