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

// SerializeSigned encodes a signed transaction as the protobuf bytes the node
// accepts on /wallet/broadcasthex. raw_data_hex already is the serialized
// Transaction.raw message, so only the field framing is added: field 1 carries
// raw_data, field 2 each signature. This is the only way to rebroadcast a
// transaction of which nothing but raw_data_hex was stored, because
// /wallet/broadcasttransaction needs the raw_data JSON object.
func SerializeSigned(tx *Transaction) (string, error) {
	if tx == nil || tx.RawDataHex == "" {
		return "", errors.New("transaction has no raw_data_hex")
	}
	if len(tx.Signature) == 0 {
		return "", errors.New("transaction has no signature")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(tx.RawDataHex, "0x"))
	if err != nil {
		return "", fmt.Errorf("raw_data_hex: %w", err)
	}
	out := appendProtoBytes(nil, 1, raw)
	for _, s := range tx.Signature {
		sig, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
		if err != nil {
			return "", fmt.Errorf("signature: %w", err)
		}
		out = appendProtoBytes(out, 2, sig)
	}
	return hex.EncodeToString(out), nil
}

// appendProtoBytes writes one length delimited protobuf field.
func appendProtoBytes(dst []byte, field int, val []byte) []byte {
	dst = appendVarint(dst, uint64(field)<<3|2)
	dst = appendVarint(dst, uint64(len(val)))
	return append(dst, val...)
}

func appendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
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
// 按照波场（TRON）TRC-20 代币合约的标准 ABI（应用二进制接口）规范，
// 将转账操作的接收方地址和转账金额手动“打包/编码”成一段十六进制的 calldata（调用数据）
// 这段生成的十六进制字符串，最终会作为智能合约交易的输入数据（Data 字段），
// 用于触发并执行 TRC-20 代币合约的 transfer(address,uint256) 转账函数
func EncodeTRC20Transfer(to string, amount *big.Int) (string, error) {
	raw, err := DecodeAddress(to)
	if err != nil {
		return "", fmt.Errorf("encode transfer: %w", err)
	}
	selector := Keccak256([]byte("transfer(address,uint256)"))[:4] //生成函数选择器
	param := make([]byte, 64)
	copy(param[12:32], raw[1:]) // 20-byte address, left padded
	amtBytes := amount.Bytes()
	if len(amtBytes) > 32 {
		return "", errors.New("amount overflows uint256")
	}
	copy(param[64-len(amtBytes):], amtBytes) //编码接收地址参数
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
