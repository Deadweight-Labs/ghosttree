package originctx

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRepoPathKeepsOnlyPathsInsideTheRepo(t *testing.T) {
	cases := []struct{ path, checkout, want string }{
		{"internal/store/x.go", "", "internal/store/x.go"},
		{"./internal/store/x.go", "", "internal/store/x.go"},
		{"/work/repo/internal/store/x.go", "/work/repo", "internal/store/x.go"},
		{"/work/repo/internal/store/x.go", "", ""},
		{"/home/me/.claude/plans/p.md", "/work/repo", ""},
		{"/work/repo", "/work/repo", ""},
		{"../other/x.go", "", ""},
		{".git/config", "", ""},
		{"internal/*.go", "", ""},
		{"internal/store/", "", "internal/store/"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := RepoPath(c.path, c.checkout); got != c.want {
			t.Errorf("RepoPath(%q, %q) = %q, want %q", c.path, c.checkout, got, c.want)
		}
	}
}

func TestBuildWeightsWrittenOverReadAndNamedOverWritten(t *testing.T) {
	c := Build("sess-1", "2026-11-07T10:00:00Z", []Touch{
		{Path: "internal/web/a.go"},
		{Path: "internal/web/b.go"},
		{Path: "internal/web/c.go"},
		{Path: "internal/store/x.go", Writes: true},
		{Path: "docs/readme.md"},
	}, nil)
	if c == nil || c.SessionRef != "sess-1" || c.CapturedAt != "2026-11-07T10:00:00Z" {
		t.Fatalf("context = %+v", c)
	}
	// store: 1 Schreiben = 3; web: 3 Lesen = 3; gleiches Gewicht: alphabetisch.
	want := []Area{{"internal/store", 3}, {"internal/web", 3}, {"docs", 1}}
	if !reflect.DeepEqual(c.Areas, want) {
		t.Fatalf("areas = %+v, want %+v", c.Areas, want)
	}
	if c.Main() != "internal/store" {
		t.Fatalf("main = %q", c.Main())
	}
	kinds := map[string]string{}
	for _, p := range c.Paths {
		kinds[p.Path] = p.Kind
	}
	if kinds["internal/store/x.go"] != KindWrite || kinds["docs/readme.md"] != KindRead {
		t.Fatalf("kinds = %v", kinds)
	}

	// Ein ausdrücklich genannter Pfad schlägt viele gelesene.
	c = Build("s", "t", []Touch{
		{Path: "internal/web/a.go"}, {Path: "internal/web/b.go"}, {Path: "internal/web/c.go"},
		{Path: "internal/web/d.go", Writes: true},
	}, []string{"cmd/ctx/main.go"})
	if c.Main() != "cmd/ctx" {
		t.Fatalf("a named path must lead: %+v", c.Areas)
	}
	for _, p := range c.Paths {
		if p.Path == "cmd/ctx/main.go" && (p.Kind != KindNamed || p.Weight != WeightNamed) {
			t.Fatalf("named path = %+v", p)
		}
	}
}

func TestBuildRepetitionAddsALimitedBonusAndTheStrongestKindWins(t *testing.T) {
	var touches []Touch
	for i := 0; i < 10; i++ {
		touches = append(touches, Touch{Path: "a/b.go"})
	}
	touches = append(touches, Touch{Path: "a/b.go", Writes: true})
	c := Build("s", "t", touches, nil)
	if len(c.Paths) != 1 || c.Paths[0].Kind != KindWrite || c.Paths[0].Weight != WeightWrite+MaxRepeatBonus {
		t.Fatalf("paths = %+v", c.Paths)
	}
}

func TestBuildCondensesToTwoLevelsAndCapsTheLists(t *testing.T) {
	c := Build("s", "t", []Touch{{Path: "web/src/components/a.tsx"}, {Path: "README.md"}}, nil)
	got := []string{}
	for _, a := range c.Areas {
		got = append(got, a.Path)
	}
	if strings.Join(got, ",") != ".,web/src" {
		t.Fatalf("areas = %v", got)
	}

	var many []Touch
	for _, d := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		for i := 0; i < 6; i++ {
			many = append(many, Touch{Path: d + "/f" + string(rune('a'+i)) + ".go"})
		}
	}
	c = Build("s", "t", many, nil)
	if len(c.Areas) != MaxAreas || len(c.Paths) != MaxPaths {
		t.Fatalf("areas=%d paths=%d, want %d and %d", len(c.Areas), len(c.Paths), MaxAreas, MaxPaths)
	}
}

func TestBuildWithoutSessionAndPathsIsNilNotGuessed(t *testing.T) {
	if c := Build("", "t", []Touch{{Path: "/elsewhere/x.go"}}, nil); c != nil {
		t.Fatalf("context = %+v, want nil", c)
	}
	// Eine bekannte Session ohne Pfade bleibt festgehalten, aber ohne Bereiche.
	if c := Build("s", "t", nil, nil); c == nil || len(c.Areas) != 0 || c.Main() != "" {
		t.Fatalf("context = %+v", c)
	}
	var none *Context
	if none.Main() != "" {
		t.Fatal("nil context must have no main area")
	}
}

func TestLabelShowsDateAndMainArea(t *testing.T) {
	c := &Context{CapturedAt: "2026-11-07T10:00:00Z", Areas: []Area{{"internal/store", 4}}}
	if got := Label(c, "2026-01-01T00:00:00Z"); got != "|on:2026-11-07|in:internal/store" {
		t.Fatalf("label = %q", got)
	}
	if got := Label(nil, "2026-01-01T00:00:00Z"); got != "|on:2026-01-01" {
		t.Fatalf("label without context = %q", got)
	}
	if got := Label(nil, ""); got != "" {
		t.Fatalf("label = %q", got)
	}
}

func TestSpecResolvesDatesRelativeTimesAndAreas(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	f, err := Spec{Since: "7d", Until: "2026-11-05", Areas: []string{"internal/store/", "./cmd"}}.Resolve(now)
	if err != nil {
		t.Fatal(err)
	}
	if f.Since != "2026-10-31T12:00:00Z" || f.Until != "2026-11-05T23:59:59Z" {
		t.Fatalf("filter = %+v", f)
	}
	if !reflect.DeepEqual(f.Areas, []string{"internal/store", "cmd"}) {
		t.Fatalf("areas = %v", f.Areas)
	}
	for in, want := range map[string]string{
		"2w": "2026-10-24T12:00:00Z", "12h": "2026-11-07T00:00:00Z",
		"2026-11-01": "2026-11-01T00:00:00Z", "2026-11-01T08:30:00+01:00": "2026-11-01T07:30:00Z",
	} {
		got, err := ParseTime(in, now, false)
		if err != nil || got != want {
			t.Errorf("ParseTime(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if empty, err := (Spec{}).Resolve(now); err != nil || empty.Active() {
		t.Fatalf("empty spec must stay inactive: %+v %v", empty, err)
	}
}

func TestSpecRejectsWhatItCannotRead(t *testing.T) {
	now := time.Now()
	for _, s := range []Spec{
		{Since: "gestern"}, {Until: "7x"}, {Since: "2026-11-05", Until: "2026-11-01"},
		{Areas: []string{"/etc"}}, {Areas: []string{"../x"}},
	} {
		if _, err := s.Resolve(now); err == nil {
			t.Errorf("%+v must be rejected, not ignored", s)
		}
	}
}
