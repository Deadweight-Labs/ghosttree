package request

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxDerivedFrom begrenzt, wie viele Quellen ein Auftrag beim Anlegen nennen darf.
const MaxDerivedFrom = 20

// Arten einer Quelle, aus der ein Auftrag entstand.
const (
	OriginRequest = "request"
	OriginIdea    = "idea"
	OriginPain    = "pain"
	OriginThread  = "thread"
)

// OriginRef ist eine Quelle in der Schreibweise des Aufrufers: REQ-n, IDEA-n,
// PAIN-n, thread:n (auch room:n, THREAD-n) oder bei Eltern-Issue auch nur die
// Zahl.
type OriginRef struct {
	Kind string
	ID   int64
}

// ParseOriginRef liest eine Quelle. Die Zahl allein gilt als Auftrag, damit
// parent=12 und parent=REQ-12 dasselbe sind.
func ParseOriginRef(s string) (OriginRef, error) {
	in := strings.TrimSpace(s)
	low := strings.ToLower(in)
	kind, rest := "", ""
	for prefix, k := range map[string]string{
		"req-": OriginRequest, "idea-": OriginIdea, "pain-": OriginPain,
		"thread-": OriginThread, "thread:": OriginThread, "room:": OriginThread,
	} {
		if strings.HasPrefix(low, prefix) {
			kind, rest = k, low[len(prefix):]
			break
		}
	}
	if kind == "" {
		kind, rest = OriginRequest, low
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return OriginRef{}, fmt.Errorf("%q is not a reference like REQ-12, IDEA-3, PAIN-7 or thread:5", in)
	}
	return OriginRef{Kind: kind, ID: n}, nil
}

// IdeaLink ist eine Idee, aus der der Auftrag entsteht. Promote sagt, ob der
// Anlegende die Idee auch annehmen darf (Bearbeitungsrecht, Idee ausgearbeitet):
// dann folgt sie dem Weg von POST /api/ideas/{id}/requests und wird angenommen,
// sonst bleibt es bei der Verknüpfung.
type IdeaLink struct {
	ID      int64
	Promote bool
}

// Links sind die geprüften Verknüpfungen eines neuen Auftrags. Der Server baut
// sie aus parent und derived_from nach der Rechteprüfung; sie kommen nie aus
// einem Request-Rumpf unmittelbar in den Store.
type Links struct {
	// Parent ist der Eltern-Issue (Relation parent: der neue Auftrag ist das Kind).
	Parent int64
	// Requests, Ideas, Pains und Threads sind die Quellen („Entstanden aus“).
	Requests []int64
	Ideas    []IdeaLink
	Pains    []int64
	Threads  []int64
	// Principal ist das Konto (person:n) des Anlegenden. Der Store prüft damit in
	// der Transaktion, dass es jeden Thread ändern darf, den es anhängt.
	Principal string
	// WebSession sagt, dass der Anlegende in einer Browser-Sitzung schreibt; dann
	// öffnet seine Projektrolle den Raum des Threads, sonst nur die Mitgliedschaft.
	WebSession bool
}

// Empty sagt, ob nichts zu verknüpfen ist.
func (l Links) Empty() bool {
	return l.Parent == 0 && len(l.Requests) == 0 && len(l.Ideas) == 0 && len(l.Pains) == 0 && len(l.Threads) == 0
}
