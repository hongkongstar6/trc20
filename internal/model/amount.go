package model

import (
	"math/big"
	"strconv"
	"strings"
)

// FormatUnits renders a minimum-unit amount as the token amount an operator
// reads ("13000000" with 6 decimals -> "13", "12345678" -> "12.345678"). It is
// what the amount column of every transfer record stores next to the raw units.
func FormatUnits(units string, decimals int) string {
	value, ok := new(big.Int).SetString(units, 10)
	if !ok || decimals < 0 {
		return units
	}
	sign := ""
	if value.Sign() < 0 {
		sign = "-"
		value = new(big.Int).Neg(value)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole, frac := new(big.Int).QuoRem(value, scale, new(big.Int))
	if decimals == 0 || frac.Sign() == 0 {
		return sign + whole.String()
	}
	digits := strings.TrimRight(padLeft(frac.String(), decimals), "0")
	return sign + whole.String() + "." + digits
}

// FormatTRX renders a TRX amount without an exponent or trailing zeros, so
// 13 is stored as "13" and 12.345678 as "12.345678".
func FormatTRX(trx float64) string {
	return strconv.FormatFloat(trx, 'f', -1, 64)
}

func padLeft(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}
