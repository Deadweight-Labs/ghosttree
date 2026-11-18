package store

import (
	"strings"
	"testing"
)

func TestFreeTextHasAnUpperBound(t *testing.T) {
	st := openTest(t)
	count := func() (n int) {
		_ = st.db.QueryRow(`SELECT COUNT(*) FROM knowledge`).Scan(&n)
		return n
	}
	longTitle := strings.Repeat("t", maxTitleRunes+1)
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: longTitle, Body: "ok"}); ruleCode(err) != "text_too_long" || count() != 0 {
		t.Fatalf("long title: %v (%d rows)", err, count())
	}
	hugeBody := strings.Repeat("b", maxBodyBytes+1)
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: "x", Body: hugeBody}); ruleCode(err) != "text_too_long" || count() != 0 {
		t.Fatalf("huge body: %v (%d rows)", err, count())
	}
	if _, err := st.InsertKnowledge(Knowledge{Type: "note", Title: strings.Repeat("t", maxTitleRunes), Body: strings.Repeat("b", 100<<10)}); err != nil {
		t.Fatalf("a long but allowed entry: %v", err)
	}
}

func TestCheckTextLengthNamesTheListField(t *testing.T) {
	err := checkTextLength("reason", strings.Repeat("r", maxNoteRunes+1))
	if ruleCode(err) != "text_too_long" || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("reason: %v", err)
	}
	// Eine Zeichenzahl, kein Bytemaß: Umlaute zählen einfach.
	if err := checkTextLength("title", strings.Repeat("ü", maxTitleRunes)); err != nil {
		t.Fatalf("umlauts at the limit: %v", err)
	}
	if err := checkTextLength("criteria[3]", "kurz"); err != nil {
		t.Fatal(err)
	}
}
