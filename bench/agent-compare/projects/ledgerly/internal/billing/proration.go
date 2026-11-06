package billing

import (
	"errors"
	"time"

	"github.com/acme-billing/ledgerly/internal/money"
)

// ErrBadPeriod is returned when a proration period is empty or inverted.
var ErrBadPeriod = errors.New("billing: invalid proration period")

// Prorate returns the share of a monthly price for the days a plan was active
// within a period of periodDays days.
func Prorate(monthly money.Cents, activeDays, periodDays int) (money.Cents, error) {
	if periodDays <= 0 || activeDays < 0 || activeDays > periodDays {
		return 0, ErrBadPeriod
	}
	return money.Round(int64(monthly)*int64(activeDays), int64(periodDays)), nil
}

// DaysBetween counts the calendar days in [from, to).
func DaysBetween(from, to time.Time) int {
	if !to.After(from) {
		return 0
	}
	return int(to.Sub(from).Hours()/24 + 0.5)
}

// PlanChangeCredit is the credit for the unused rest of the old plan when a
// customer switches plans mid-period.
func PlanChangeCredit(oldMonthly money.Cents, usedDays, periodDays int) (money.Cents, error) {
	used, err := Prorate(oldMonthly, usedDays, periodDays)
	if err != nil {
		return 0, err
	}
	return oldMonthly - used, nil
}
