package query

import "testing"

func TestMessageAndSessionAreKinds(t *testing.T) {
	for line, kind := range map[string]string{
		"kind:message": KindMessage, "is:messages": KindMessage, "kind:nachricht": KindMessage,
		"kind:session": KindSession, "is:sessions": KindSession,
	} {
		q := mustParse(t, line)
		if ks := q.Kinds(); len(ks) != 1 || ks[0] != kind {
			t.Errorf("%s: kinds = %v, want %s", line, ks, kind)
		}
	}
	q := mustParse(t, "-kind:message")
	if q.WantsKind(KindMessage) || !q.WantsKind(KindSession) {
		t.Error("-kind:message leaves out messages only")
	}
	// Weder Nachricht noch Session hat einen Zustand: is:open trifft sie nie.
	for _, k := range []string{KindMessage, KindSession} {
		if match(t, "is:open", Facts{Kind: k}) || match(t, "is:closed", Facts{Kind: k}) {
			t.Errorf("%s has no state class", k)
		}
	}
	if !match(t, "branch:feat/x repo:dw/p", Facts{Kind: KindSession, Repo: "github.com/dw/p", Branches: []string{"feat/x"}}) {
		t.Error("a session answers branch: and repo:")
	}
}
