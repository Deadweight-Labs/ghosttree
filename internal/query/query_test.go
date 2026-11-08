package query

import (
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func mustParse(t *testing.T, line string) Query {
	t.Helper()
	q, err := Parse(line, testNow)
	if err != nil {
		t.Fatalf("Parse(%q): %v", line, err)
	}
	return q
}

func TestFreeTextAndQuotedPhrases(t *testing.T) {
	q := mustParse(t, `embedding "cold start" is:open https://example.com/a REQ-5`)
	if q.Text != `embedding "cold start" https://example.com/a REQ-5` {
		t.Fatalf("text = %q", q.Text)
	}
	if len(q.Terms) != 1 || q.Terms[0].Key != KeyIs {
		t.Fatalf("terms = %+v", q.Terms)
	}
}

func TestUnknownQualifierIsAVisibleError(t *testing.T) {
	for _, line := range []string{"flavor:sweet", "is:oen", "kind:wiki", "-hello", "label:", `label:"x`, "commit:abc", "pr:x", "created:yesterday", "req:0", "area:/etc", "area:../x", "pr:5..1", "created:2026-12-01..2026-11-01"} {
		_, err := Parse(line, testNow)
		if err == nil {
			t.Errorf("Parse(%q) accepted a bad line", line)
			continue
		}
		var qe *Error
		if e, ok := err.(*Error); ok {
			qe = e
		}
		if qe == nil || qe.Msg == "" {
			t.Errorf("Parse(%q): error without message: %v", line, err)
		}
	}
	_, err := Parse("flavor:sweet", testNow)
	if err == nil || !strings.Contains(err.Error(), "known qualifiers") {
		t.Fatalf("unknown key should list the known ones: %v", err)
	}
}

func TestControlCharactersInNamesAreRejected(t *testing.T) {
	if _, err := Parse("label:a\x07b", testNow); err == nil {
		t.Fatal("control character accepted")
	}
}

func match(t *testing.T, line string, f Facts) bool {
	t.Helper()
	return mustParse(t, line).Match(f)
}

func TestIsQualifier(t *testing.T) {
	cases := []struct {
		line string
		f    Facts
		want bool
	}{
		{"is:open", Facts{Kind: KindRequest, State: "open"}, true},
		{"is:open", Facts{Kind: KindRequest, State: "done"}, false},
		{"is:open", Facts{Kind: KindPain, State: "mitigated"}, true},
		{"is:done", Facts{Kind: KindPain, State: "resolved"}, true},
		{"is:done", Facts{Kind: KindCriterion, State: "met"}, true},
		{"is:closed", Facts{Kind: KindRequest, State: "done"}, true},
		{"is:closed", Facts{Kind: KindRequest, State: "dropped"}, true},
		{"is:closed", Facts{Kind: KindRequest, State: "open"}, false},
		{"is:done", Facts{Kind: KindIdea, State: "accepted"}, true},
		{"is:open", Facts{Kind: KindKnowledge, State: "active"}, false},
		{"-is:open", Facts{Kind: KindKnowledge, State: "active"}, true},
		{"is:resolved", Facts{Kind: KindPain, State: "resolved"}, true},
		{"is:dismissed", Facts{Kind: KindIdea, State: "dismissed"}, true},
		{"is:pain", Facts{Kind: KindPain, State: "active"}, true},
		{"is:pain", Facts{Kind: KindRequest, State: "open"}, false},
		{"is:idea", Facts{Kind: KindIdea}, true},
		{"is:issue", Facts{Kind: KindRequest}, true},
		{"is:knowledge", Facts{Kind: KindKnowledge}, true},
		{"is:done,dropped", Facts{Kind: KindRequest, State: "dropped"}, true},
		{"is:open is:pain", Facts{Kind: KindRequest, State: "open"}, false},
	}
	for _, c := range cases {
		if got := match(t, c.line, c.f); got != c.want {
			t.Errorf("%q on %+v = %v, want %v", c.line, c.f, got, c.want)
		}
	}
}

func TestKindQualifier(t *testing.T) {
	if !match(t, "kind:task", Facts{Kind: KindTask}) || match(t, "kind:task", Facts{Kind: KindPain}) {
		t.Fatal("kind:")
	}
	if !match(t, "-kind:file", Facts{Kind: KindPain}) {
		t.Fatal("-kind:")
	}
}

func TestKindsNarrowTheSearch(t *testing.T) {
	q := mustParse(t, "is:pain label:x")
	if got := q.Kinds(); len(got) != 1 || got[0] != KindPain {
		t.Fatalf("Kinds = %v", got)
	}
	if q := mustParse(t, "label:x"); q.Kinds() != nil {
		t.Fatal("no kind restriction expected")
	}
	if q := mustParse(t, "req:5"); !q.WantsKind(KindRequest) || q.WantsKind(KindPain) {
		t.Fatal("req: restricts to requests")
	}
	if q := mustParse(t, "-is:idea"); q.WantsKind(KindIdea) || !q.WantsKind(KindPain) {
		t.Fatal("-is:idea drops ideas")
	}
	if q := mustParse(t, "is:pain is:idea"); len(q.Kinds()) != 0 || q.Kinds() == nil {
		t.Fatalf("two disjoint kinds leave nothing: %v", q.Kinds())
	}
	if q := mustParse(t, "pain:1 idea:2"); !q.WantsKind(KindPain) || !q.WantsKind(KindIdea) || q.WantsKind(KindRequest) {
		t.Fatalf("numbers of different kinds are alternatives: %v", q.Kinds())
	}
}

func TestTypeQualifier(t *testing.T) {
	if !match(t, "type:bug", Facts{Type: "bug"}) || match(t, "type:bug", Facts{Type: "feature"}) || match(t, "type:bug", Facts{}) {
		t.Fatal("type:")
	}
	if !match(t, "type:bug,feature", Facts{Type: "Feature"}) {
		t.Fatal("type: alternatives, case-insensitive")
	}
}

func TestRepoQualifier(t *testing.T) {
	f := Facts{Repo: "github.com/deadweight-labs/ghosttree"}
	if !match(t, "repo:deadweight-labs/ghosttree", f) || !match(t, "repo:github.com/deadweight-labs/ghosttree", f) {
		t.Fatal("repo: suffix and full")
	}
	if match(t, "repo:other/ghosttree", f) || match(t, "repo:tree", f) {
		t.Fatal("repo: must match at a path boundary")
	}
	if !match(t, "-repo:other/x", f) {
		t.Fatal("-repo:")
	}
	if got := mustParse(t, "repo:a/b").Repo(); got != "a/b" {
		t.Fatalf("Repo() = %q", got)
	}
}

func TestBranchQualifier(t *testing.T) {
	f := Facts{Branches: []string{"feat/git-native", "main"}}
	if !match(t, "branch:feat/git-native", f) || match(t, "branch:feat/other", f) {
		t.Fatal("branch:")
	}
	if !match(t, "-branch:dev", f) || match(t, "-branch:main", f) {
		t.Fatal("-branch:")
	}
}

func TestCommitQualifier(t *testing.T) {
	f := Facts{Commits: []string{"e505ae9aaaabbbb"}}
	if !match(t, "commit:e505ae9", f) || !match(t, "commit:E505AE9AA", f) || match(t, "commit:1795ca2", f) {
		t.Fatal("commit:")
	}
	if !match(t, "-commit:1795ca2", f) {
		t.Fatal("-commit:")
	}
	if got := mustParse(t, "commit:e505ae9").Commit(); got != "e505ae9" {
		t.Fatalf("Commit() = %q", got)
	}
}

func TestPRQualifierAndRanges(t *testing.T) {
	f := Facts{PRs: []int64{12}}
	for line, want := range map[string]bool{
		"pr:12": true, "pr:#12": true, "pr:13": false, "pr:10..14": true, "pr:13..": false,
		"pr:..12": true, "pr:>11": true, "pr:>12": false, "pr:<=12": true, "pr:<12": false, "pr:1,12": true,
		"-pr:12": false, "-pr:13": true,
	} {
		if got := match(t, line, f); got != want {
			t.Errorf("%q = %v, want %v", line, got, want)
		}
	}
	if mustParse(t, "pr:12").PR() != 12 || mustParse(t, "pr:1..5").PR() != 0 {
		t.Fatal("PR() is the single exact number only")
	}
}

func TestPackageQualifier(t *testing.T) {
	f := Facts{Packages: []int64{4}}
	if !match(t, "package:4", f) || !match(t, "package:PKG-4", f) || match(t, "package:5", f) {
		t.Fatal("package:")
	}
}

func TestNumberQualifiersPerKind(t *testing.T) {
	if !match(t, "req:596", Facts{Kind: KindRequest, Number: 596}) || match(t, "req:596", Facts{Kind: KindPain, Number: 596}) {
		t.Fatal("req:")
	}
	if !match(t, "req:REQ-596", Facts{Kind: KindCriterion, Number: 596}) {
		t.Fatal("req: also names the criteria of a request")
	}
	if !match(t, "task:124", Facts{Kind: KindTask, Number: 124}) || match(t, "task:124", Facts{Kind: KindRequest, Number: 124}) {
		t.Fatal("task:")
	}
	if !match(t, "pain:57", Facts{Kind: KindPain, Number: 57}) || !match(t, "idea:3", Facts{Kind: KindIdea, Number: 3}) {
		t.Fatal("pain:/idea:")
	}
	if !match(t, "req:590..600", Facts{Kind: KindRequest, Number: 596}) || match(t, "req:590..595", Facts{Kind: KindRequest, Number: 596}) {
		t.Fatal("range")
	}
	if !match(t, "req:1 pain:2", Facts{Kind: KindPain, Number: 2}) {
		t.Fatal("numbers of different kinds are alternatives")
	}
	if match(t, "req:1 pain:2", Facts{Kind: KindPain, Number: 3}) {
		t.Fatal("wrong number")
	}
	if match(t, "-pain:57", Facts{Kind: KindPain, Number: 57}) || !match(t, "-pain:57", Facts{Kind: KindPain, Number: 58}) {
		t.Fatal("-pain:")
	}
}

func TestParentAndBlocksQualifiers(t *testing.T) {
	f := Facts{Parents: []int64{602}, Blocks: []int64{599}}
	if !match(t, "parent:602", f) || match(t, "parent:599", f) || !match(t, "blocks:REQ-599", f) || match(t, "blocks:1", f) {
		t.Fatal("parent:/blocks:")
	}
	if !match(t, "-parent:1", f) {
		t.Fatal("-parent:")
	}
}

func TestAuthorAndForQualifiers(t *testing.T) {
	f := Facts{Author: "Robin", Assignee: "Jev"}
	if !match(t, "author:robin", f) || match(t, "author:jev", f) || !match(t, "for:JEV", f) || !match(t, "assignee:jev", f) || match(t, "for:robin", f) {
		t.Fatal("author:/for:")
	}
	q := mustParse(t, "author:@me for:@me").ResolveMe("Robin")
	if !q.Match(Facts{Author: "robin", Assignee: "ROBIN"}) {
		t.Fatal("@me resolves to the asker")
	}
	if q.Match(Facts{Author: "jev", Assignee: "robin"}) {
		t.Fatal("@me must not match someone else")
	}
}

func TestLabelQualifiers(t *testing.T) {
	f := Facts{Labels: []string{"Bug", "good first issue"}}
	if !match(t, "label:bug", f) || !match(t, `label:"good first issue"`, f) || match(t, "label:docs", f) {
		t.Fatal("label:")
	}
	if !match(t, "label:docs,bug", f) {
		t.Fatal("label: alternatives")
	}
	if match(t, "-label:bug", f) || !match(t, "-label:docs", f) {
		t.Fatal("-label:")
	}
	if !match(t, "-label:bug", Facts{}) {
		t.Fatal("-label: on an unlabelled hit")
	}
	if !match(t, "label:bug label:\"good first issue\"", f) || match(t, "label:bug label:docs", f) {
		t.Fatal("two label: qualifiers both have to hold")
	}
}

func TestAreaQualifiers(t *testing.T) {
	f := Facts{Areas: []string{"internal/store"}}
	for line, want := range map[string]bool{
		"area:internal/store": true, "area:internal": true, "area:internal/store/sub": true, "area:internal/storex": false,
		"area:internal/web": false, "-area:internal/web": true, "-area:internal": false, "area:internal/web,internal/store": true,
		`area:./internal/store/`: true,
	} {
		if got := match(t, line, f); got != want {
			t.Errorf("%q = %v, want %v", line, got, want)
		}
	}
	if got := mustParse(t, "area:a area:b").Origin().Areas; len(got) != 2 {
		t.Fatalf("Origin areas = %v", got)
	}
	if got := mustParse(t, "-area:a").Origin().Areas; len(got) != 0 {
		t.Fatal("a negated area is not a SQL prefilter")
	}
}

func TestTopicQualifierResolvesToAreas(t *testing.T) {
	q := mustParse(t, "topic:search")
	if got := q.Topics(); len(got) != 1 || got[0] != "search" {
		t.Fatalf("Topics = %v", got)
	}
	r, err := q.ResolveTopics(func(n string) ([]string, bool) {
		if n == "search" {
			return []string{"internal/embed", "internal/store/embeddings"}, true
		}
		return nil, false
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Match(Facts{Areas: []string{"internal/embed"}}) || !r.Match(Facts{Areas: []string{"internal/store/embeddings"}}) || r.Match(Facts{Areas: []string{"internal/web"}}) {
		t.Fatal("topic: acts like several area prefixes")
	}
	if _, err := mustParse(t, "topic:nope").ResolveTopics(func(string) ([]string, bool) { return nil, false }); err == nil {
		t.Fatal("an unknown topic must be an error")
	}
	neg, _ := mustParse(t, "-topic:search").ResolveTopics(func(string) ([]string, bool) { return []string{"internal/embed"}, true })
	if neg.Match(Facts{Areas: []string{"internal/embed"}}) || !neg.Match(Facts{Areas: []string{"internal/web"}}) {
		t.Fatal("-topic:")
	}
}

func TestCreatedAndUpdatedQualifiers(t *testing.T) {
	f := Facts{Created: "2026-11-05T10:00:00Z", Updated: "2026-11-07T10:00:00Z"}
	for line, want := range map[string]bool{
		"created:2026-11-05": true, "created:2026-11-06": false, "created:>2026-11-04": true, "created:>2026-11-05": false,
		"created:>=2026-11-05": true, "created:<2026-11-06": true, "created:<2026-11-05": false, "created:<=2026-11-05": true,
		"created:2026-11-01..2026-11-05": true, "created:2026-11-06..": false, "created:..2026-11-05": true,
		"updated:>2026-11-06": true, "updated:<2026-11-06": false, "-updated:<2026-11-06": true,
		"updated:>1d": false, "updated:>7d": true, "created:>=2026-11-05T10:00:00Z": true,
	} {
		if got := match(t, line, f); got != want {
			t.Errorf("%q = %v, want %v", line, got, want)
		}
	}
	if match(t, "created:>2026-01-01", Facts{}) {
		t.Fatal("a hit without a date matches no date")
	}
}

func TestSinceAndUntilUseTheOriginTime(t *testing.T) {
	f := Facts{Created: "2026-11-01T00:00:00Z", OriginAt: "2026-11-06T09:00:00Z"}
	if !match(t, "since:2026-11-05", f) || match(t, "until:2026-11-05", f) {
		t.Fatal("since:/until: take the origin time")
	}
	if got := mustParse(t, "since:2026-11-05 until:2026-11-07").Origin(); got.Since != "2026-11-05T00:00:00Z" || got.Until != "2026-11-07T23:59:59Z" {
		t.Fatalf("Origin = %+v", got)
	}
}

func TestQualifiersCombineWithAnd(t *testing.T) {
	f := Facts{Kind: KindPain, State: "active", Labels: []string{"bug"}, Areas: []string{"internal/web"}}
	if !match(t, "is:pain is:open label:bug area:internal", f) {
		t.Fatal("all hold")
	}
	if match(t, "is:pain is:open label:bug -area:internal/web", f) {
		t.Fatal("one fails")
	}
}

func TestStringRoundTrip(t *testing.T) {
	q := mustParse(t, `foo is:open -label:"good first issue"`)
	if got := q.String(); got != `foo is:open -label:"good first issue"` {
		t.Fatalf("String = %q", got)
	}
}
