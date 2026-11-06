// Package money holds the monetary primitives used across ledgerly.
package money

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Cents is an amount of money in euro cents.
type Cents int64

// Zero is the zero amount.
const Zero Cents = 0

// FromEuros builds an amount from whole euros and cents.
func FromEuros(euros int64, cents int64) Cents {
	if euros < 0 {
		return Cents(euros*100 - cents)
	}
	return Cents(euros*100 + cents)
}

// Add returns a + b.
func (c Cents) Add(o Cents) Cents { return c + o }

// Sub returns a - b.
func (c Cents) Sub(o Cents) Cents { return c - o }

// Neg returns -c.
func (c Cents) Neg() Cents { return -c }

// IsNegative reports whether c < 0.
func (c Cents) IsNegative() bool { return c < 0 }

// Abs returns the absolute value.
func (c Cents) Abs() Cents {
	if c < 0 {
		return -c
	}
	return c
}

// Min returns the smaller of two amounts.
func Min(a, b Cents) Cents {
	if a < b {
		return a
	}
	return b
}

// Max returns the larger of two amounts.
func Max(a, b Cents) Cents {
	if a > b {
		return a
	}
	return b
}

// Sum adds up a list of amounts.
func Sum(amounts ...Cents) Cents {
	var total Cents
	for _, a := range amounts {
		total += a
	}
	return total
}

// String renders the amount as "12.34 EUR".
func (c Cents) String() string {
	sign := ""
	v := int64(c)
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d EUR", sign, v/100, v%100)
}

// FormatDE renders the amount the way German invoices print it: "1.234,56 €".
func (c Cents) FormatDE() string {
	sign := ""
	v := int64(c)
	if v < 0 {
		sign = "-"
		v = -v
	}
	whole := strconv.FormatInt(v/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("%s%s,%02d €", sign, b.String(), v%100)
}

// ErrParse is returned for malformed amounts.
var ErrParse = errors.New("money: cannot parse amount")

// Parse reads "12.34", "12,34" or "12" into cents.
func Parse(s string) (Cents, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "€"))
	s = strings.TrimSpace(strings.TrimSuffix(s, "EUR"))
	if s == "" {
		return 0, ErrParse
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	s = strings.ReplaceAll(s, ",", ".")
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return 0, ErrParse
	}
	euros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, ErrParse
	}
	var cents int64
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) == 1 {
			frac += "0"
		}
		if len(frac) != 2 {
			return 0, ErrParse
		}
		cents, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, ErrParse
		}
	}
	v := Cents(euros*100 + cents)
	if neg {
		v = -v
	}
	return v, nil
}
