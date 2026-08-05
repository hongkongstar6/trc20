package tron

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

// AddressPrefix is the TRON mainnet/testnet address prefix byte (0x41).
const AddressPrefix = 0x41

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// Keccak256 is the hash used to derive an address from a public key.
func Keccak256(data ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}

func doubleSHA256(b []byte) []byte {
	first := sha256.Sum256(b)
	second := sha256.Sum256(first[:])
	return second[:]
}

func base58Encode(input []byte) string {
	x := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)
	var out []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		out = append(out, b58Alphabet[mod.Int64()])
	}
	for _, b := range input {
		if b != 0 {
			break
		}
		out = append(out, b58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(s string) ([]byte, error) {
	x := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range s {
		idx := strings.IndexRune(b58Alphabet, r)
		if idx < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", r)
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(int64(idx)))
	}
	decoded := x.Bytes()
	var leading int
	for _, r := range s {
		if r != rune(b58Alphabet[0]) {
			break
		}
		leading++
	}
	return append(make([]byte, leading), decoded...), nil
}

// EncodeAddress converts a 21-byte raw address (0x41 + 20 bytes) to base58check.
func EncodeAddress(raw []byte) (string, error) {
	if len(raw) != 21 || raw[0] != AddressPrefix {
		return "", errors.New("invalid raw tron address")
	}
	checksum := doubleSHA256(raw)[:4]
	return base58Encode(append(append([]byte{}, raw...), checksum...)), nil
}

// DecodeAddress converts a base58check address into its 21-byte raw form.
func DecodeAddress(addr string) ([]byte, error) {
	decoded, err := base58Decode(addr)
	if err != nil {
		return nil, err
	}
	if len(decoded) != 25 {
		return nil, fmt.Errorf("invalid address length %d", len(decoded))
	}
	raw, checksum := decoded[:21], decoded[21:]
	if raw[0] != AddressPrefix {
		return nil, errors.New("invalid address prefix")
	}
	if !bytes.Equal(doubleSHA256(raw)[:4], checksum) {
		return nil, errors.New("invalid address checksum")
	}
	return raw, nil
}

// IsValidAddress reports whether addr is a well formed base58check T-address.
func IsValidAddress(addr string) bool {
	if !strings.HasPrefix(addr, "T") {
		return false
	}
	_, err := DecodeAddress(addr)
	return err == nil
}

// AddressToHex returns the 41-prefixed hex form used by the node HTTP API.
func AddressToHex(addr string) (string, error) {
	raw, err := DecodeAddress(addr)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// HexToAddress converts a node hex address (with or without 0x/41 prefix) to base58.
func HexToAddress(h string) (string, error) {
	h = strings.TrimPrefix(strings.ToLower(h), "0x")
	raw, err := hex.DecodeString(h)
	if err != nil {
		return "", err
	}
	switch len(raw) {
	case 21:
	case 20:
		raw = append([]byte{AddressPrefix}, raw...)
	case 32:
		// Left padded 32-byte word from an event topic.
		raw = append([]byte{AddressPrefix}, raw[12:]...)
	default:
		return "", fmt.Errorf("unexpected address hex length %d", len(raw))
	}
	return EncodeAddress(raw)
}

// PubKeyToAddress derives the base58 address from an uncompressed public key.
func PubKeyToAddress(pub []byte) (string, error) {
	if len(pub) == 65 {
		pub = pub[1:]
	}
	if len(pub) != 64 {
		return "", fmt.Errorf("unexpected public key length %d", len(pub))
	}
	hash := Keccak256(pub)
	raw := append([]byte{AddressPrefix}, hash[12:]...)
	return EncodeAddress(raw)
}
