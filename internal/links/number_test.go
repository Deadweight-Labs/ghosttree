package links

import (
	"slices"
	"testing"
)

func TestParseNumberReadsEveryFormOfANumberTerm(t *testing.T) {
	for in, want := range map[string]NumberTerm{
		"#217":      {N: 217},
		"  #217  ":  {N: 217},
		"REQ-12":    {N: 12, Kind: KindRequest},
		"req-12":    {N: 12, Kind: KindRequest},
		" Req-12\t": {N: 12, Kind: KindRequest},
		"PAIN-5":    {N: 5, Kind: KindPain},
		"pain-5":    {N: 5, Kind: KindPain},
		"TASK-3":    {N: 3, Kind: KindTask},
		"task-3":    {N: 3, Kind: KindTask},
		"IDEA-7":    {N: 7, Kind: KindIdea},
		"KN-12":     {N: 12, Kind: KindKnowledge},
		"kn-12":     {N: 12, Kind: KindKnowledge},
		"Idea-7":    {N: 7, Kind: KindIdea},
		"pr:217":    {N: 217, Kind: KindPull},
		"PR:217":    {N: 217, Kind: KindPull},
		"pr:#217":   {N: 217, Kind: KindPull},
		"pr: 217":   {N: 217, Kind: KindPull},
		"pull:217":  {N: 217, Kind: KindPull},
		" pr:217 ":  {N: 217, Kind: KindPull},
	} {
		got, ok := ParseNumber(in)
		if !ok || got != want {
			t.Errorf("ParseNumber(%q) = %+v, %v; want %+v", in, got, ok, want)
		}
	}
}

func TestParseNumberLeavesEverythingElseToTheSearch(t *testing.T) {
	for _, in := range []string{
		"", "#", "#0", "#-3", "#12a", "# 12", "##12", "217", "REQ-", "REQ-0", "REQ-12 login", "login REQ-12", "REQ-12#2", "AC-4.2",
		"FEAT-3", "DEC-4", "pr:", "pr:0", "pr:12 13", "pr:abc", "pr:1234567890123456", "#1234567890123456", "commit:abcdef1", "is:open",
	} {
		if got, ok := ParseNumber(in); ok {
			t.Errorf("ParseNumber(%q) = %+v, want no number", in, got)
		}
	}
}

func TestNumberTermKindsAreTheOneItNamesOrTheHashKinds(t *testing.T) {
	hash, _ := ParseNumber("#7")
	if !slices.Equal(hash.Kinds(), []string{KindRequest, KindPull, KindKnowledge}) {
		t.Errorf("#7 means %v", hash.Kinds())
	}
	for _, kind := range hash.Kinds() {
		if !hash.Is(kind) {
			t.Errorf("#7 is not %s", kind)
		}
	}
	if hash.Is(KindTask) {
		t.Error("#7 may not be a task: tasks have TASK-7")
	}
	one, _ := ParseNumber("TASK-7")
	if !slices.Equal(one.Kinds(), []string{KindTask}) || one.Is(KindRequest) {
		t.Errorf("TASK-7 means %v", one.Kinds())
	}
	var shorts []string
	for _, r := range hash.Refs() {
		shorts = append(shorts, Short(r))
	}
	if !slices.Equal(shorts, []string{"REQ-7", "pr:7", "KN-7"}) {
		t.Errorf("short forms of #7: %v", shorts)
	}
}

// The references of a page, the hover cards and the terminal read their numbers
// through ParseRef; it has to agree with the search on what a number is.
func TestParseRefAgreesWithParseNumber(t *testing.T) {
	for _, in := range []string{"REQ-12", "req-12", "PAIN-5", "TASK-3", "IDEA-7", "pr:217", "pull:217", "PR: 217", "pr:#217"} {
		term, ok := ParseNumber(in)
		if !ok {
			t.Fatalf("%q is no number", in)
		}
		ref, err := ParseRef(in)
		if err != nil || ref.Kind != term.Kind || ref.ID != term.N {
			t.Errorf("ParseRef(%q) = %+v, %v; the number is %+v", in, ref, err, term)
		}
	}
	// In a text and in the terminal #N is a knowledge entry; a search asks for every kind.
	if ref, err := ParseRef("#4295"); err != nil || ref.Kind != KindKnowledge || ref.ID != 4295 {
		t.Errorf("ParseRef(#4295) = %+v, %v", ref, err)
	}
	// What is no number stays what it was.
	for in, kind := range map[string]string{"FEAT-3": KindFeature, "DEC-4": KindDecision, "REQ-5#2": KindCriterion} {
		if ref, err := ParseRef(in); err != nil || ref.Kind != kind {
			t.Errorf("ParseRef(%q) = %+v, %v; want %s", in, ref, err, kind)
		}
	}
}
