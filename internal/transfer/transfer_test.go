package transfer

import "testing"

// An operator reads these numbers to check a payout against the chain, so the
// rendering has to be exact: no rounding, no lost trailing digits, and the raw
// units back untouched when they cannot be rendered at all.
func TestAmountText(t *testing.T) {
	cases := []struct {
		units    string
		decimals int
		want     string
	}{
		{"1000000", 6, "1"},
		{"1500000", 6, "1.5"},
		{"1", 6, "0.000001"},
		{"1000001", 6, "1.000001"},
		{"1000010", 6, "1.00001"},
		{"0", 6, "0"},
		{"12", 0, "12"},
		{"12", -1, "12"},
		{"not a number", 6, "not a number"},
	}
	for _, c := range cases {
		if got := AmountText(c.units, c.decimals); got != c.want {
			t.Errorf("AmountText(%q, %d) = %q, want %q", c.units, c.decimals, got, c.want)
		}
	}
}

// A reason longer than the column would fail the whole update, which would lose
// the failure itself.
func TestTruncateCapsTheReason(t *testing.T) {
	if got := Truncate("short", 240); got != "short" {
		t.Fatalf("a reason that fits must be kept as is, got %q", got)
	}
	if got := Truncate("0123456789", 4); got != "0123" {
		t.Fatalf("Truncate = %q, want %q", got, "0123")
	}
}

// The fee is affordable exactly when the balance covers it: an address holding
// the cost to the sun must not be refused.
func TestBurnBudgetEnough(t *testing.T) {
	if !(BurnBudget{CostSun: 27_000_000, BalanceSun: 27_000_000}).Enough() {
		t.Fatal("a balance equal to the cost must be enough")
	}
	if (BurnBudget{CostSun: 27_000_000, BalanceSun: 26_999_999}).Enough() {
		t.Fatal("a balance below the cost must not be enough")
	}
}
