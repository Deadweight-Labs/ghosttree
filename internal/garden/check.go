package garden

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/llmusage"
)

// Verdict kinds. Only Stale and Correction ever lead to a proposal.
const (
	Holds      = "holds"
	Stale      = "stale"
	Correction = "correction"
	Unclear    = "unclear"
	Skipped    = "skipped" // nothing in the entry can be looked up; no call was made
	Failed     = "failed"
	NotRun     = "not-run" // budget or a limit ran out before this entry
)

const (
	// MaxOutputTokens caps one answer; it is also what the budget check
	// assumes the answer costs before it exists. It has to leave room for the
	// model's own thinking, which counts against the same limit: at 900 a
	// pilot lost 15 of 25 answers to thinking that used it all up. Only what
	// is actually generated is billed.
	MaxOutputTokens = 4096
	// ExpectedOutputTokens is the typical answer, used only for the dry-run
	// estimate next to the upper bound.
	ExpectedOutputTokens = 350
	// charsPerToken is a deliberate underestimate (German text and code run
	// near three), so the estimate errs towards spending less than it says.
	charsPerToken = 3
	// ContextChars bounds the code context per entry.
	ContextChars = 14000
	maxFailures  = 3
)

func EstimateTokens(chars int) int { return chars / charsPerToken }

// Pricing is USD per million tokens. The cache prices are optional: when 0
// they follow the input price (reads a tenth, writes 1.25 times).
type Pricing struct{ InUSDPerMTok, OutUSDPerMTok, CacheReadUSDPerMTok, CacheWriteUSDPerMTok float64 }

// Cost is the cost of plain input and output tokens, as the dry run estimates it.
func (p Pricing) Cost(in, out int) float64 {
	return p.CostUsage(llm.Usage{InputTokens: in, OutputTokens: out})
}

// CostUsage is the cost of one call as the provider reported it: cache reads and
// writes at their own prices, not at the input price and not for free.
func (p Pricing) CostUsage(u llm.Usage) float64 {
	return llm.Price{InUSD: p.InUSDPerMTok, OutUSD: p.OutUSDPerMTok, CacheReadUSD: p.CacheReadUSDPerMTok, CacheWriteUSD: p.CacheWriteUSDPerMTok}.Cost(u)
}

type Verdict struct {
	Entry      Entry
	Kind       string
	Reason     string
	Evidence   []string // citations found in the supplied context
	Unverified []string // citations the model made that are not in it
	Correction string
	Files      []string
	Symbols    []string
	In, Out    int // tokens: provider-reported, or estimated for a dry run
	// CacheRead and CacheWrite are the provider's cache token counts; they are
	// not part of In.
	CacheRead, CacheWrite int
	CostUSD               float64
}

type Options struct {
	Model   llm.UsageClient // nil only with DryRun
	Pricing Pricing
	MaxUSD  float64
	DryRun  bool
	// Limit caps how many entries are sent to the model (0 = all). Entries
	// that cannot be looked up cost nothing and do not count.
	Limit int
	// Gate is asked before every call and names the cap (day or month, in
	// dollars, calls or tokens) that is used up, or returns nil. The run stops
	// cleanly at the first one it names.
	Gate func() *llmusage.Reached
	// OnCall sees every call that was made, with what the provider reported.
	OnCall func(v Verdict, u llm.Usage, failed bool)
	// OnJudged sees every entry that got a verdict from the model, so the
	// caller can remember that it was looked at.
	OnJudged func(v Verdict)
}

// Result is a whole run. Spent is what the provider reported (or, in a dry
// run, the estimate); UpperBoundUSD is the dry-run worst case.
type Result struct {
	Verdicts      []Verdict
	Spent         float64
	UpperBoundUSD float64
	StoppedBy     string // "" | "budget" | "limit" | "errors"
	// LimitHit says which cap it was when StoppedBy is "limit".
	LimitHit string
	DryRun   bool
	MaxUSD   float64
	// Rotation says what was left out before the run because it was not due.
	Rotation Rotation
}

const systemPrompt = `Du prüfst einen Wissens-Eintrag (Pitfall oder Decision) eines Softwareprojekts gegen den aktuellen Code. Du entscheidest nichts, du machst einen Vorschlag, den ein Mensch prüft.

Urteile ausschließlich aus dem gelieferten Code-Kontext. Was dort nicht belegt ist, ist "unclear". Erfinde keine Commits, Dateien oder Zeilen.

Antworte mit genau einem JSON-Objekt und sonst nichts:
{"verdict":"holds|stale|correction|unclear","reason":"...","evidence":["..."],"correction":"..."}

- holds: Die Aussage gilt nach Code und Commits weiter.
- stale: Der Gegenstand existiert nicht mehr oder die Aussage ist durch spätere Änderungen überholt.
- correction: Im Kern gültig, aber ein Detail stimmt nicht mehr. "correction" enthält den korrigierten Text; sonst weglassen.
- unclear: Der Kontext reicht für kein Urteil.
- reason: ein bis drei Sätze, die konkrete Commits (Hash) oder Datei:Zeile nennen.
- evidence: nur Commit-Hashes (so lang wie im Kontext, mindestens 10 Zeichen) und exakte Datei:Zeile ohne Bereiche, die wörtlich im Kontext stehen. Bei stale und correction ist Evidenz Pflicht.
- Eine historische Entscheidung oder ein Pitfall der Form "wir haben X an Datum bewusst nicht getan" bleibt gültig, solange die Begründung zutrifft, auch wenn der Code sich weiterentwickelt hat.`

func userPrompt(e Entry, cc CodeContext) string {
	return fmt.Sprintf("EINTRAG #%d (%s), beobachtet %s\nTitel: %s\n\n%s\n\n=== CODE-KONTEXT (aus dem Checkout) ===\n%s",
		e.ID, e.Type, e.Since(), e.Title, strings.TrimSpace(e.Body), cc.Text)
}

// Check judges every entry in order. The budget is tested before each call
// against the worst case of that call, so a run never ends over the cap by more
// than a single answer's difference between worst case and actual.
func Check(ctx context.Context, repo *Repo, entries []Entry, opt Options) Result {
	res := Result{DryRun: opt.DryRun, MaxUSD: opt.MaxUSD}
	failures, sent := 0, 0
	for i, e := range entries {
		cc, gerr := repo.Gather(e, ContextChars)
		v := Verdict{Entry: e, Files: cc.Files, Symbols: cc.Symbols}
		if gerr != nil {
			// Could not look is not the same as nothing there: no model call,
			// and no verdict that would read as "gone".
			v.Kind, v.Reason = Failed, "Code-Kontext nicht lesbar, kein Modellaufruf: "+gerr.Error()
			res.Verdicts = append(res.Verdicts, v)
			continue
		}
		if !cc.HasRefs() {
			v.Kind, v.Reason = Skipped, "der Eintrag nennt weder Datei noch Symbol, das sich im Code nachschlagen ließe"
			res.Verdicts = append(res.Verdicts, v)
			if opt.OnJudged != nil && !opt.DryRun {
				opt.OnJudged(v)
			}
			continue
		}
		in := EstimateTokens(len(systemPrompt) + len(userPrompt(e, cc)))
		worst := opt.Pricing.Cost(in, MaxOutputTokens)
		if opt.Limit > 0 && sent >= opt.Limit {
			v.Kind, v.Reason = NotRun, "Anzahl-Limit erreicht, nicht geprüft"
			res.Verdicts = append(res.Verdicts, v)
			continue
		}
		sent++
		if opt.DryRun {
			v.Kind, v.In, v.Out = NotRun, in, ExpectedOutputTokens
			v.CostUSD = opt.Pricing.Cost(in, ExpectedOutputTokens)
			res.Spent += v.CostUSD
			res.UpperBoundUSD += worst
			res.Verdicts = append(res.Verdicts, v)
			continue
		}
		if opt.Gate != nil {
			if hit := opt.Gate(); hit != nil {
				res.StoppedBy, res.LimitHit = "limit", hit.Error()
				for _, rest := range entries[i:] {
					res.Verdicts = append(res.Verdicts, Verdict{Entry: rest, Kind: NotRun, Reason: "Grenze erreicht, nicht geprüft"})
				}
				return res
			}
		}
		if res.Spent+worst > opt.MaxUSD {
			res.StoppedBy = "budget"
			for _, rest := range entries[i:] {
				res.Verdicts = append(res.Verdicts, Verdict{Entry: rest, Kind: NotRun, Reason: "Budget erreicht, nicht geprüft"})
			}
			return res
		}
		text, usage, err := opt.Model.CompleteWithUsage(ctx, systemPrompt, []llm.Message{{Role: "user", Content: userPrompt(e, cc)}}, MaxOutputTokens)
		v.In, v.Out, v.CacheRead, v.CacheWrite = usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens
		v.CostUSD = opt.Pricing.CostUsage(usage)
		if err != nil && usage.InputTokens+usage.OutputTokens+usage.CacheReadTokens+usage.CacheWriteTokens == 0 {
			// A timeout or a 5xx may still have been billed and reports no
			// usage. Count the worst case rather than nothing.
			v.In, v.Out, v.CostUSD = in, MaxOutputTokens, worst
			usage = llm.Usage{InputTokens: in, OutputTokens: MaxOutputTokens}
		}
		res.Spent += v.CostUSD
		if opt.OnCall != nil {
			opt.OnCall(v, usage, err != nil)
		}
		if err != nil {
			failures++
			v.Kind, v.Reason = Failed, err.Error()
			res.Verdicts = append(res.Verdicts, v)
			if failures >= maxFailures {
				res.StoppedBy = "errors"
				for _, rest := range entries[i+1:] {
					res.Verdicts = append(res.Verdicts, Verdict{Entry: rest, Kind: NotRun, Reason: "Lauf nach wiederholten Fehlern abgebrochen"})
				}
				return res
			}
			continue
		}
		failures = 0
		applyAnswer(&v, text, cc)
		res.Verdicts = append(res.Verdicts, v)
		if opt.OnJudged != nil && v.Kind != Failed {
			opt.OnJudged(v)
		}
	}
	return res
}

type answer struct {
	Verdict    string   `json:"verdict"`
	Reason     string   `json:"reason"`
	Evidence   []string `json:"evidence"`
	Correction string   `json:"correction"`
}

// applyAnswer parses the model's reply and holds it to its own citations: a
// stale or correction verdict whose evidence is not in the supplied context is
// the model talking, not the code, so it is downgraded to unclear.
func applyAnswer(v *Verdict, text string, cc CodeContext) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	var a answer
	if start < 0 || end < start || json.Unmarshal([]byte(text[start:end+1]), &a) != nil {
		v.Kind, v.Reason = Failed, "Antwort war kein lesbares JSON"
		return
	}
	v.Reason, v.Correction = strings.TrimSpace(a.Reason), strings.TrimSpace(a.Correction)
	v.Evidence, v.Unverified = splitEvidence(cc, a.Evidence)
	switch a.Verdict {
	case Holds, Unclear:
		v.Kind = a.Verdict
	case Stale, Correction:
		v.Kind = a.Verdict
		if len(v.Evidence) == 0 {
			v.Kind = Unclear
			v.Reason = "Unbelegt, das Modell nannte keine im Kontext auffindbare Evidenz (Vorschlag war: " + a.Verdict + "). " + v.Reason
		}
		if v.Kind == Correction && v.Correction == "" {
			v.Kind = Unclear
			v.Reason = "Korrektur ohne Text. " + v.Reason
		}
	default:
		v.Kind, v.Reason = Failed, fmt.Sprintf("unbekanntes Urteil %q", a.Verdict)
	}
}

var (
	lineRef    = regexp.MustCompile(`^([^\s:]+):(\d+)$`)
	fullHashRe = regexp.MustCompile(`^[0-9a-f]{10,40}$`)
)

// splitEvidence separates citations the context supports from the rest. Only
// two shapes count: an exact file:line that appears on a line of the context,
// and a commit hash of at least ten characters that is a prefix of, or extends,
// one in the context's log. Free text, ranges and short hashes do not.
func splitEvidence(cc CodeContext, refs []string) (ok, bad []string) {
	for _, r := range refs {
		r = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r), "commit "))
		if r == "" {
			continue
		}
		if verifies(cc, r) {
			ok = append(ok, r)
		} else {
			bad = append(bad, r)
		}
	}
	return ok, bad
}

func verifies(cc CodeContext, ref string) bool {
	if fullHashRe.MatchString(ref) {
		for _, h := range cc.Hashes {
			if strings.HasPrefix(ref, h) || strings.HasPrefix(h, ref) {
				return true
			}
		}
		return false
	}
	if m := lineRef.FindStringSubmatch(ref); m != nil {
		return cc.Lines[m[1]+":"+m[2]]
	}
	return false
}
