package money

// Rounding helpers. Amounts are computed as a rational number num/den and
// rounded to whole cents.

// Round rounds num/den to the nearest cent, halves away from zero.
func Round(num, den int64) Cents {
	if den == 0 {
		panic("money: division by zero")
	}
	if den < 0 {
		num, den = -num, -den
	}
	q, r := num/den, num%den
	if r < 0 {
		r = -r
	}
	if 2*r >= den {
		if num < 0 {
			q--
		} else {
			q++
		}
	}
	return Cents(q)
}

// RoundBankers rounds num/den to the nearest cent, halves to even.
func RoundBankers(num, den int64) Cents {
	if den == 0 {
		panic("money: division by zero")
	}
	if den < 0 {
		num, den = -num, -den
	}
	q, r := num/den, num%den
	if r < 0 {
		r = -r
	}
	switch {
	case 2*r > den:
		if num < 0 {
			q--
		} else {
			q++
		}
	case 2*r == den:
		if q%2 != 0 {
			if num < 0 {
				q--
			} else {
				q++
			}
		}
	}
	return Cents(q)
}

// BasisPoints is a percentage in hundredths of a percent (250 = 2.5 %).
type BasisPoints int64

// Percent builds basis points from a whole percentage.
func Percent(p int64) BasisPoints { return BasisPoints(p * 100) }
