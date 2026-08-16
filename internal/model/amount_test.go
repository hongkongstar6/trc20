package model

import "testing"

func TestFormatUnits(t *testing.T) {
	cases := []struct {
		units    string
		decimals int
		want     string
	}{
		{"13000000", 6, "13"},
		{"12345678", 6, "12.345678"},
		{"1", 6, "0.000001"},
		{"1000010", 6, "1.00001"},
		{"0", 6, "0"},
		{"13", 0, "13"},
		{"-12345678", 6, "-12.345678"},
		{"not-a-number", 6, "not-a-number"},
	}
	for _, c := range cases {
		if got := FormatUnits(c.units, c.decimals); got != c.want {
			t.Errorf("FormatUnits(%q, %d) = %q, want %q", c.units, c.decimals, got, c.want)
		}
	}
}

func TestFormatTRX(t *testing.T) {
	cases := []struct {
		trx  float64
		want string
	}{
		{13, "13"},
		{12.345678, "12.345678"},
		{0, "0"},
		{0.0000001, "0.0000001"},
	}
	for _, c := range cases {
		if got := FormatTRX(c.trx); got != c.want {
			t.Errorf("FormatTRX(%v) = %q, want %q", c.trx, got, c.want)
		}
	}
}
