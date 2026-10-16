package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
)

// WithGardenLimits tells the page which caps the gardener runs under. The
// gardener is a command, not part of the server, so the server only shows them;
// it reads them from the same variables and llm.json the command does.
func WithGardenLimits(l llmusage.Limits) Option {
	return func(a *app) { a.gardenLimits = l }
}

// gardenAdminView is the administrator's view of the gardener.
type gardenAdminView struct {
	Usage   string
	Limit   string
	Checked string
}

// money writes dollars the way the language does.
func (l lang) money(v float64) string {
	if l == langDE {
		return strings.Replace(fmt.Sprintf("%.2f", v), ".", ",", 1) + " $"
	}
	return fmt.Sprintf("$%.2f", v)
}

// usagePart renders one figure of a period: the amount, and the cap when there
// is one. It returns "" for a figure that is neither used nor capped.
func usagePart(l lang, figure string, used float64, limit float64) string {
	if used == 0 && limit == 0 {
		return ""
	}
	have, cap := fmt.Sprintf("%d", int64(used)), fmt.Sprintf("%d", int64(limit))
	if figure == "usd" {
		have, cap = l.money(used), l.money(limit)
	}
	switch figure {
	case "usd":
		if limit > 0 {
			return l.msg("usage.of", have, cap)
		}
		return have
	case "calls":
		if limit > 0 {
			return l.msg("usage.part.calls_of", have, cap)
		}
		if used == 1 {
			return l.msg("usage.part.call_one")
		}
		return l.msg("usage.part.calls", have)
	}
	if limit > 0 {
		return l.msg("usage.part.tokens_of", have, cap)
	}
	return l.msg("usage.part.tokens", have)
}

func periodText(l lang, u llmusage.Usage, lim llmusage.Limit) string {
	var parts []string
	for _, p := range []string{
		usagePart(l, "usd", u.CostUSD(), lim.USD),
		usagePart(l, "calls", float64(u.Calls), float64(lim.Calls)),
		usagePart(l, "tokens", float64(u.Tokens()), float64(lim.Tokens)),
	} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return l.msg("usage.none")
	}
	return strings.Join(parts, ", ")
}

// usageSentence is the one sentence about a job and, when a cap is used up,
// the line that names it.
func usageSentence(l lang, job string, day, month llmusage.Usage, lim llmusage.Limits) (sentence, limit string) {
	name := l.msg("usage.job.explain")
	if job == llmusage.JobGarden {
		name = l.msg("usage.job.garden")
	}
	sentence = l.msg("usage.sentence", name, periodText(l, day, lim.Day), periodText(l, month, lim.Month))
	if r := lim.Check(day, month); r != nil {
		period := l.msg("usage.limit.day")
		if r.Period == "month" {
			period = l.msg("usage.limit.month")
		}
		limit = l.msg("usage.limit", period, usagePart(l, r.Figure, r.Used, r.Limit))
	}
	return sentence, limit
}

func (a *app) gardenAdmin(l lang) *gardenAdminView {
	now := time.Now().UTC()
	day, errD := a.store.LLMUsageOn(llmusage.JobGarden, now.Format("2006-01-02"))
	month, errM := a.store.LLMUsageMonth(llmusage.JobGarden, now.Format("2006-01"))
	if errD != nil || errM != nil {
		return nil
	}
	v := &gardenAdminView{}
	v.Usage, v.Limit = usageSentence(l, llmusage.JobGarden, day, month, a.gardenLimits)
	if n, oldest, err := a.store.GardenCheckSummary(); err == nil && n > 0 {
		v.Checked = l.msg("garden.admin.checked", n, dayOf(oldest))
	}
	return v
}

func (a *app) gardenAdminFor(r *http.Request) *gardenAdminView {
	if !a.isAdmin(browserPrincipal(r)) {
		return nil
	}
	return a.gardenAdmin(a.langFor(r))
}

// dayOf is the date part of a stored timestamp.
func dayOf(stamp string) string {
	if len(stamp) >= 10 {
		return stamp[:10]
	}
	return stamp
}
