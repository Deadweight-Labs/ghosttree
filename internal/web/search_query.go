package web

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/originctx"
	"github.com/Deadweight-Labs/ghosttree/internal/query"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Die Suchsprache auf der Seite „Suche“ (REQ-600 K8): dieselbe Zeile wie in
// context_search und der CLI, geparst von internal/query. Was die Datenbank
// schon beantwortet (Anker, Entstehungszeit und -bereich), geht als Filter in die
// Abfrage der jeweiligen Art; query.Match sortiert danach aus und ist die
// Definition. Ein unbekannter Qualifier wird gemeldet statt still zu Text zu
// werden, ebenso einer, den Gästen die Seite nicht anbietet.

// searchFetch ist, wie viele Zeilen je Art geholt werden, wenn Qualifier danach
// noch aussortieren.
const searchFetch = 200

// guestQualifiers sind die Qualifier, die Gästen nicht angeboten werden: sie
// verrieten Branch-Namen, Arbeitspakete oder Namen von Zuständigen, die ein
// Gast nicht lesen darf. branch: gilt für einen Gast nur mit dem Standard-Branch.
var guestQualifiers = []string{query.KeyBranch, query.KeyPackage, query.KeyFor}

// webKinds sind die Arten, die die Seite in Gruppen zeigt; für die anderen
// (Kriterien, Features, Dateien, Nachrichten) gibt es API, MCP und CLI.
var webKinds = []string{query.KindRequest, query.KindIdea, query.KindPain, query.KindTask, query.KindKnowledge, query.KindSession, query.KindDocument}

// searchLine ist eine Suchzeile, bereit zum Lauf.
type searchLine struct {
	text      string
	q         query.Query
	qualified bool
	anchor    store.AnchorFilter
	origin    originctx.Filter
	fetch     int
	project   string
}

// searchProblem ist, was an einer Zeile nicht geht: ein Schlüssel der Meldung
// und ihre Angaben.
type searchProblem struct {
	Key  string
	Args []any
}

// hits sagt, ob eine Art gesucht wird.
func (s searchLine) wants(kind string) bool { return s.q.WantsKind(kind) }

// parseSearchLine liest eine Suchzeile für den Betrachter. project ist das
// gewählte Projekt der Seite; repo: in der Zeile ersetzt es. Ohne Qualifier
// bleibt alles beim bisherigen Weg: der Text sucht, nichts wird aussortiert.
func (a *app) parseSearchLine(line string, pa *store.ProjectAccess, guest bool, project, person string, now time.Time) (searchLine, *searchProblem) {
	line = strings.TrimSpace(line)
	q, err := query.Parse(line, now)
	if err != nil {
		var qe *query.Error
		if errors.As(err, &qe) {
			if qe.Unknown != "" {
				return searchLine{}, &searchProblem{Key: "search.err.unknown", Args: []any{qe.Unknown + ":", strings.Join(query.KnownKeys(), ", ")}}
			}
			return searchLine{}, &searchProblem{Key: "search.err.invalid", Args: []any{qe.Msg}}
		}
		return searchLine{}, &searchProblem{Key: "search.err.invalid", Args: []any{err.Error()}}
	}
	sl := searchLine{q: q, text: q.Text, qualified: q.Qualified(), project: project, fetch: searchPerGroup + 1}
	if !sl.qualified {
		return sl, nil
	}
	sl.fetch = searchFetch
	if repo := q.Repo(); repo != "" {
		sl.project = repoRemoteOf(repo)
	}
	// Gast ist, wer im Projekt der Zeile nicht alle Branches sieht; ohne Projekt
	// zählt die höchste Rolle, und die Eigenschaften werden je Treffer gefiltert.
	if sl.project != "" {
		guest = !pa.SeesAllBranches(sl.project)
	}
	if guest {
		for _, key := range guestQualifiers {
			if !q.Has(key) {
				continue
			}
			// Der Standard-Branch des Projekts verrät nichts, was der Gast nicht ohnehin sieht.
			if key == query.KeyBranch && sl.project != "" && guestBranchOnlyDefault(a, q, sl.project) {
				continue
			}
			return searchLine{}, &searchProblem{Key: "search.err.guest", Args: []any{key + ":"}}
		}
	}
	if len(q.Topics()) > 0 {
		if sl.project == "" {
			return searchLine{}, &searchProblem{Key: "search.err.topic_project"}
		}
		// Wer das Projekt nicht lesen darf, erfährt auch nicht, welche Themen es gibt.
		if pa.Check(sl.project, store.ResLabel, store.ActRead, store.Object{}) != nil {
			return searchLine{}, &searchProblem{Key: "search.err.topic_unknown", Args: []any{strings.Join(q.Topics(), ", ")}}
		}
		resolved, err := q.ResolveTopics(pa.TopicPrefixes(sl.project))
		if err != nil {
			return searchLine{}, &searchProblem{Key: "search.err.topic_unknown", Args: []any{strings.Join(q.Topics(), ", ")}}
		}
		q = resolved
	}
	q = q.ResolveMe(person)
	sl.q, sl.text = q, q.Text
	filter, err := q.Origin().Resolve(now)
	if err != nil {
		return searchLine{}, &searchProblem{Key: "search.err.invalid", Args: []any{err.Error()}}
	}
	sl.origin = filter
	sl.anchor = store.AnchorFilter{Commit: q.Commit(), PR: q.PR()}
	if err := sl.anchor.Valid(); err != nil {
		return searchLine{}, &searchProblem{Key: "search.err.invalid", Args: []any{err.Error()}}
	}
	return sl, nil
}

// guestBranchOnlyDefault: jeder branch:-Wert der Zeile ist der Standard-Branch.
func guestBranchOnlyDefault(a *app, q query.Query, project string) bool {
	known, _ := a.store.ProjectBranches(project)
	def := store.PickDefaultBranch(known)
	vals := q.Values(query.KeyBranch)
	if len(vals) == 0 {
		return false
	}
	for _, t := range q.Terms {
		if t.Key == query.KeyBranch && t.Neg {
			return false
		}
	}
	for _, v := range vals {
		if !strings.EqualFold(v, def) {
			return false
		}
	}
	return true
}

// repoRemoteOf macht aus repo:owner/name die Remote des Projekts.
func repoRemoteOf(v string) string {
	v = strings.Trim(strings.TrimSpace(v), "/")
	first, _, _ := strings.Cut(v, "/")
	if !strings.Contains(first, ".") {
		v = "github.com/" + v
	}
	return scope.NormalizeRemote(v)
}

// unsupportedKinds nennt die Arten, die die Zeile will und die die Seite nicht
// in Gruppen zeigt. Nur wenn die Zeile eine Art ausdrücklich nennt.
func (s searchLine) unsupportedKinds() []string {
	ks := s.q.Kinds()
	if ks == nil {
		return nil
	}
	var out []string
	for _, k := range ks {
		if !slices.Contains(webKinds, k) && s.q.WantsKind(k) {
			out = append(out, k)
		}
	}
	return out
}

// factsOf baut die Eigenschaften eines Treffers für query.Match.
// Branches, Pakete und Zuständige sieht die Abfrage nur so weit, wie der Betrachter
// sie im Projekt des Treffers lesen darf (ProjectAccess.FactsView).
func factsOf(pa *store.ProjectAccess, kind string, number int64, state, typ, repo, author, assignee, created, updated string, of store.ObjectFacts) query.Facts {
	of, assignee = pa.FactsView(repo, of), pa.AssigneeView(repo, assignee)
	return query.Facts{
		Kind: kind, State: state, Type: typ, Repo: repo, Number: number, Author: author, Assignee: assignee,
		Branches: of.Branches, Commits: of.Commits, PRs: of.PRs, Packages: of.Packages, Labels: of.Labels,
		Areas: of.Areas, Parents: of.Parents, Blocks: of.Blocks, Features: of.Features,
		Created: created, Updated: updated, OriginAt: of.OriginAt,
		Actor: of.Actor, Harness: of.Harness, Model: of.Model,
	}
}

// hitOrigin ist Datum und Hauptbereich eines Treffers: der Zeitpunkt des
// Entstehungskontexts (sonst der des Anlegens) und der schwerste Bereich.
type hitOrigin struct {
	Date, Stamp, Area string
}

// originOf liest Datum und Hauptbereich aus den Eigenschaften und dem Zeitpunkt des Anlegens.
func originOf(l lang, now time.Time, created string, of store.ObjectFacts) hitOrigin {
	stamp := of.OriginAt
	if stamp == "" {
		stamp = created
	}
	h := hitOrigin{Stamp: stamp}
	if t := parseTime(stamp); !t.IsZero() {
		h.Date = l.date(t.UTC(), t.UTC().Year() != now.Year())
	}
	if len(of.Areas) > 0 {
		h.Area = of.Areas[0]
	}
	return h
}
