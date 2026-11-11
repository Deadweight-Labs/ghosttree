package web

import (
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Sessions an PR, Commit und Request (IDEA-31 P4): der Block „Entstanden in“ der
// Seitenspalte. Was eine Zeile zeigt, entscheidet der Store (GitSessionLinks, DEC-11
// A): Titel und Adresse nur mit Transkriptrecht, sonst Person und Datum, alle
// anderen eine Zahl. Hier wird nur gesetzt, nie nachgesehen.

// sessLine ist eine Session im Block.
type sessLine struct {
	// Href ist leer ohne Transkriptrecht: dann steht nur Person und Datum da.
	Href, Title, Person, Date, Note string
	// Weak: nur über ein Zeitfenster zugeordnet, ein Kandidat und kein Beleg.
	Weak bool
}

// sessBlock ist der Block einer Seite; nil, wenn nichts zu zeigen ist.
type sessBlock struct {
	Lines []sessLine
	// More und Hidden sind die Zahlen unter der Liste, fertig formuliert.
	More, Hidden string
}

// sessionBlockOf macht aus den Verweisen, die der Betrachter sehen darf, den Block.
// ctx sagt, auf welcher Seite er steht: pr, commit oder request.
func sessionBlockOf(l lang, links store.GitSessionLinks, ctx string) *sessBlock {
	if len(links.Items) == 0 && links.Hidden == 0 && links.More == 0 {
		return nil
	}
	b := &sessBlock{}
	for _, it := range links.Items {
		line := sessLine{Person: it.Person, Weak: it.Weak, Note: sessionNote(l, it, ctx)}
		if it.Level == store.GitSessionLinkFull {
			line.Href = "/ui/sessions/" + it.SessionRef
			line.Title = strings.TrimSpace(it.Title)
			if line.Title == "" {
				line.Title = l.msg("sess.untitled")
			}
			line.Title = ellipsize(line.Title, 90)
		}
		if t, err := time.Parse("2006-01-02", it.Date); err == nil {
			line.Date = l.date(t, true)
		}
		b.Lines = append(b.Lines, line)
	}
	if links.More > 0 {
		b.More = l.msg("sess.more", links.More)
	}
	if links.Hidden > 0 {
		b.Hidden = l.msg("sess.hidden", links.Hidden)
	}
	return b
}

// sessionNote sagt in einem Satzstück, woher die Verbindung kommt: was die Session
// an dieser Stelle getan hat.
func sessionNote(l lang, it store.GitSessionLinkView, ctx string) string {
	var parts []string
	switch ctx {
	case "request":
		for _, o := range it.Origins {
			switch o {
			case store.RequestOriginPR:
				if it.PR > 0 {
					parts = append(parts, l.msg("sess.origin_pr", it.PR))
				}
			case store.RequestOriginEvidence:
				parts = append(parts, l.msg("sess.origin_evidence"))
			}
		}
	case "commit":
		switch it.Via {
		case store.LinkViaPR:
			if it.PR > 0 {
				parts = append(parts, l.msg("sess.via_pr", it.PR))
			}
		case store.LinkViaPatch:
			parts = append(parts, l.msg("sess.via_patch"))
		}
	default:
		for _, s := range it.Sources {
			switch s {
			case store.PRSourceCreated:
				parts = append(parts, l.msg("sess.created_pr"))
			case store.PRSourceMerged:
				parts = append(parts, l.msg("sess.merged_pr"))
			}
		}
		if it.Commits > 0 {
			parts = append(parts, l.msg("sess.n_commits", it.Commits))
		}
	}
	return strings.Join(parts, " · ")
}

// pullSessionBlock und commitSessionBlock holen die Verweise mit der Sichtprüfung
// des Betrachters. Ein Fehler lässt den Block weg: die Seite steht auch ohne ihn.
func pullSessionBlock(l lang, pa *store.ProjectAccess, project string, n int64) *sessBlock {
	links, err := pa.PullSessions(project, n)
	if err != nil {
		return nil
	}
	return sessionBlockOf(l, links, "pr")
}

func commitSessionBlock(l lang, pa *store.ProjectAccess, project, sha string) *sessBlock {
	links, err := pa.CommitSessions(project, sha)
	if err != nil {
		return nil
	}
	return sessionBlockOf(l, links, "commit")
}

func requestSessionBlock(l lang, pa *store.ProjectAccess, id int64) *sessBlock {
	links, err := pa.RequestSessions(id)
	if err != nil {
		return nil
	}
	return sessionBlockOf(l, links, "request")
}
