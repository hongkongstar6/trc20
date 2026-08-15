// Package hd derives TRON keys from a BIP39 mnemonic using BIP32/BIP44.
// Only sign-service imports this package; no other service may.
package hd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	bip39 "github.com/tyler-smith/go-bip39"
	"golang.org/x/text/unicode/norm"

	"github.com/hongkongstar6/trc20/internal/tron"
)

// CoinTypeTRON is the BIP44 coin type for TRON. Solana would be 501, which is
// why the path is configuration driven rather than hard coded at call sites.
const CoinTypeTRON = 195

// Wallet holds the master key derived from the mnemonic seed.
type Wallet struct {
	master *hdkeychain.ExtendedKey //seed 派生出的 BIP32 主密钥
}

// NormalizeMnemonic canonicalises a mnemonic the way BIP39 requires before it
// is used as PBKDF2 input: NFKD normalization and exactly one ASCII space
// between words. Word list validation splits on any whitespace, but the seed is
// derived from the raw string, so a mnemonic pasted with a double space, a tab,
// a newline or an ideographic space still validates while producing a seed that
// no standard wallet (TronLink, imToken, Ledger) would compute.
func NormalizeMnemonic(mnemonic string) string {
	return strings.Join(strings.Fields(norm.NFKD.String(mnemonic)), " ")
}

// NormalizePassphrase applies the NFKD normalization BIP39 mandates for the
// optional passphrase.
func NormalizePassphrase(passphrase string) string {
	return norm.NFKD.String(passphrase)
}

// NewFromMnemonic validates the mnemonic and builds the master key.
func NewFromMnemonic(mnemonic, passphrase string) (*Wallet, error) {
	mnemonic = NormalizeMnemonic(mnemonic)
	if mnemonic == "" {
		return nil, errors.New("hd: empty mnemonic")
	}
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, errors.New("hd: invalid mnemonic checksum")
	}
	seed := bip39.NewSeed(mnemonic, NormalizePassphrase(passphrase))
	master, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		return nil, fmt.Errorf("hd: new master: %w", err)
	}
	return &Wallet{master: master}, nil
}

// DeriveAddress returns the base58check TRON address for a path.
func (w *Wallet) DeriveAddress(path string) (string, error) {
	priv, err := w.DerivePrivateKey(path)
	if err != nil {
		return "", err
	}
	defer priv.Zero()
	return tron.PubKeyToAddress(priv.PubKey().SerializeUncompressed())
}

// DerivePrivateKey walks a path such as m/44'/195'/0'/0/12.
func (w *Wallet) DerivePrivateKey(path string) (*btcec.PrivateKey, error) {
	indexes, err := ParsePath(path)
	if err != nil {
		return nil, err
	}
	key := w.master
	for _, idx := range indexes {
		key, err = key.Derive(idx)
		if err != nil {
			return nil, fmt.Errorf("hd: derive %s: %w", path, err)
		}
	}
	priv, err := key.ECPrivKey()
	if err != nil {
		return nil, fmt.Errorf("hd: ec priv key: %w", err)
	}
	return priv, nil
}

// ParsePath converts a BIP32 path string into child indexes.
func ParsePath(path string) ([]uint32, error) {
	parts := strings.Split(strings.TrimSpace(path), "/")
	if len(parts) == 0 || parts[0] != "m" {
		return nil, fmt.Errorf("hd: path must start with m/: %q", path)
	}
	out := make([]uint32, 0, len(parts)-1)
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		hardened := strings.HasSuffix(p, "'") || strings.HasSuffix(p, "h")
		p = strings.TrimRight(p, "'h")
		v, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("hd: bad path element %q: %w", p, err)
		}
		idx := uint32(v)
		if hardened {
			idx += hdkeychain.HardenedKeyStart
		}
		out = append(out, idx)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("hd: empty path %q", path)
	}
	return out, nil
}

// AddressPath joins an account level path with a child index.
func AddressPath(accountPath string, index int64) string {
	return fmt.Sprintf("%s/%d", strings.TrimRight(accountPath, "/"), index)
}
