package tron

import (
	"strings"
	"testing"
)

// The mainnet USDT-TRC20 contract is a well known address/hex pair, so it
// doubles as a base58check vector.
const (
	usdtAddress = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	usdtHex     = "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"
)

func TestAddressToHex(t *testing.T) {
	got, err := AddressToHex(usdtAddress)
	if err != nil {
		t.Fatalf("AddressToHex: %v", err)
	}
	if !strings.EqualFold(got, usdtHex) {
		t.Fatalf("got %s, want %s", got, usdtHex)
	}
}

func TestHexToAddressRoundTrip(t *testing.T) {
	addr, err := HexToAddress(usdtHex)
	if err != nil {
		t.Fatalf("HexToAddress: %v", err)
	}
	if addr != usdtAddress {
		t.Fatalf("got %s, want %s", addr, usdtAddress)
	}
}

func TestIsValidAddress(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want bool
	}{
		{"valid", usdtAddress, true},
		{"empty", "", false},
		{"bad checksum", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6u", false},
		{"wrong prefix", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2", false},
		{"not base58", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6!", false},
		{"truncated", "TR7NHqjeKQxGTCi8q8ZY4pL8ot", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsValidAddress(c.addr); got != c.want {
				t.Fatalf("IsValidAddress(%q) = %v, want %v", c.addr, got, c.want)
			}
		})
	}
}

func TestEncodeAddressRejectsNonTronPayload(t *testing.T) {
	// Only the 21 byte 0x41 prefixed form is a TRON account; anything else
	// would encode into a plausible looking but unspendable address.
	bad := [][]byte{
		append([]byte{0x42}, make([]byte, 20)...), // wrong prefix
		append([]byte{0x41}, make([]byte, 19)...), // too short
		{},
	}
	for i, raw := range bad {
		if _, err := EncodeAddress(raw); err == nil {
			t.Fatalf("case %d: expected an error", i)
		}
	}
}
