package hd

import (
	"strings"
	"testing"

	"github.com/hongkongstar6/trc20/internal/tron"
)

// A throwaway mnemonic used only by the tests.
const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

func TestParsePath(t *testing.T) {
	got, err := ParsePath("m/44'/195'/0'/0/7")
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	want := []uint32{44 + 1<<31, CoinTypeTRON + 1<<31, 1 << 31, 0, 7}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestParsePathRejectsGarbage(t *testing.T) {
	for _, path := range []string{"", "44'/195'", "m/44'/x/0", "m/44'/195'/0'/0/-1"} {
		if _, err := ParsePath(path); err == nil {
			t.Fatalf("ParsePath(%q) should have failed", path)
		}
	}
}

func TestAddressPath(t *testing.T) {
	if got := AddressPath("m/44'/195'/0'/0", 12); got != "m/44'/195'/0'/0/12" {
		t.Fatalf("got %s", got)
	}
	// A trailing slash in the config must not produce a double slash.
	if got := AddressPath("m/44'/195'/0'/0/", 12); got != "m/44'/195'/0'/0/12" {
		t.Fatalf("got %s", got)
	}
}

func TestDeriveAddressIsDeterministicAndValid(t *testing.T) {
	w, err := NewFromMnemonic(testMnemonic, "")
	if err != nil {
		t.Fatalf("NewFromMnemonic: %v", err)
	}
	first, err := w.DeriveAddress("m/44'/195'/0'/0/0")
	if err != nil {
		t.Fatalf("DeriveAddress: %v", err)
	}
	again, err := w.DeriveAddress("m/44'/195'/0'/0/0")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatalf("derivation is not deterministic: %s vs %s", first, again)
	}
	if !tron.IsValidAddress(first) {
		t.Fatalf("derived address %s is not a valid TRON address", first)
	}
	second, err := w.DeriveAddress("m/44'/195'/0'/0/1")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("different indexes derived the same address")
	}
}

// The address handed to the business system must belong to the key that will
// later sign for it, otherwise deposits land somewhere unspendable.
func TestDeriveAddressMatchesPrivateKey(t *testing.T) {
	w, err := NewFromMnemonic(testMnemonic, "")
	if err != nil {
		t.Fatal(err)
	}
	const path = "m/44'/195'/0'/0/3"
	addr, err := w.DeriveAddress(path)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := w.DerivePrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	fromKey, err := tron.PubKeyToAddress(priv.PubKey().SerializeUncompressed())
	if err != nil {
		t.Fatal(err)
	}
	if addr != fromKey {
		t.Fatalf("address %s does not match the key derived address %s", addr, fromKey)
	}
}

// The BIP39 test vector address every wallet (TronLink included) shows for this
// mnemonic. It pins the derivation so a seed level regression cannot slip in.
func TestDeriveAddressMatchesBIP39Vector(t *testing.T) {
	w, err := NewFromMnemonic(testMnemonic, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.DeriveAddress("m/44'/195'/0'/0/0")
	if err != nil {
		t.Fatal(err)
	}
	const want = "TUEZSdKsoDHQMeZwihtdoBiN46zxhGWYdH"
	if got != want {
		t.Fatalf("derived %s, want %s", got, want)
	}
}

// A mnemonic pasted with double spaces, tabs, newlines or ideographic spaces
// still validates, so without normalization it would derive a different -- and
// from the wallet owner's point of view wrong -- address.
func TestIrregularWhitespaceDerivesTheSameAddress(t *testing.T) {
	want, err := NewFromMnemonic(testMnemonic, "")
	if err != nil {
		t.Fatal(err)
	}
	wantAddr, err := want.DeriveAddress("m/44'/195'/0'/0/0")
	if err != nil {
		t.Fatal(err)
	}
	messy := []string{
		"  " + testMnemonic + "\n",
		strings.Replace(testMnemonic, "abandon abandon", "abandon  abandon", 1),
		strings.Replace(testMnemonic, "abandon about", "abandon\tabout", 1),
		strings.ReplaceAll(testMnemonic, " ", "\u3000"),
		strings.ReplaceAll(testMnemonic, " ", "\u00a0"),
	}
	for _, m := range messy {
		w, err := NewFromMnemonic(m, "")
		if err != nil {
			t.Fatalf("NewFromMnemonic(%q): %v", m, err)
		}
		got, err := w.DeriveAddress("m/44'/195'/0'/0/0")
		if err != nil {
			t.Fatal(err)
		}
		if got != wantAddr {
			t.Fatalf("mnemonic %q derived %s, want %s", m, got, wantAddr)
		}
	}
}

func TestPassphraseChangesTheSeed(t *testing.T) {
	a, err := NewFromMnemonic(testMnemonic, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFromMnemonic(testMnemonic, "extra")
	if err != nil {
		t.Fatal(err)
	}
	addrA, _ := a.DeriveAddress("m/44'/195'/0'/0/0")
	addrB, _ := b.DeriveAddress("m/44'/195'/0'/0/0")
	if addrA == addrB {
		t.Fatal("passphrase had no effect on derivation")
	}
}

func TestNewFromMnemonicRejectsInvalidMnemonic(t *testing.T) {
	if _, err := NewFromMnemonic("not a valid mnemonic at all", ""); err == nil {
		t.Fatal("expected a checksum error")
	}
	if _, err := NewFromMnemonic("", ""); err == nil {
		t.Fatal("expected an error for an empty mnemonic")
	}
}
