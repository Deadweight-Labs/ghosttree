package web

import (
	"net/http"
	"slices"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Benachrichtigungen im Profil (P13.6). ghosttree hat keinen Versandweg außerhalb
// der Oberfläche, deshalb gibt es je Art genau einen Schalter: ob sie im Posteingang
// steht. Eine Art, die ein Konto abschaltet, fehlt dort ganz; sie bleibt in ihrer
// Quelle (Raum, Aufgabe, Issue) lesbar.
//
// Die Glocke zählt genau die Zeilen, die der Posteingang unter "Alle" zeigt (inboxFor):
// was auf die Person wartet und was ihr gesagt wurde, über alle Repos. Eine Zahl, die
// kleiner ist als die Liste, hinter die sie führt, sah aus wie ein Fehler (N13). Die
// Räume des Kontos werden dafür je Anfrage einmal gelesen (inboxFor ist je Anfrage
// gemerkt). Alle fünf Arten folgen dem Schalter: waitingFor-Zeilen über notifyKeep,
// Erwähnungen über notifyOff in inboxMentions und inboxTextMentions.

// notifyKeys sind die Texte der Arten.
var notifyKeys = map[string]string{store.NotifyMention: "notify.mention", store.NotifyQuestion: "notify.question",
	store.NotifyDecision: "notify.decision", store.NotifyTask: "notify.task", store.NotifyGardener: "notify.gardener"}

// notifyKindOf sagt, zu welcher Art eine Zeile des Posteingangs gehört: dieselbe
// Reihenfolge wie bei den Filtern des Posteingangs (inboxCats).
func notifyKindOf(f forYouRow) string {
	switch {
	case f.taskID != 0:
		return store.NotifyTask
	case f.decisionID != 0:
		return store.NotifyDecision
	case f.Mark == "garden":
		return store.NotifyGardener
	case f.attentionID != 0 && (f.Mark == "ask" || f.Mark == "approve" || f.Mark == "warn"):
		return store.NotifyQuestion
	}
	// Übergaben und Wissensvorschläge sind Arbeit, die auf die Person wartet.
	return store.NotifyTask
}

// notifyOff are the kinds the viewer switched off; nothing for a session without an account.
func (a *app) notifyOff(r *http.Request) map[string]bool {
	off, err := a.store.NotifyOff(browserPrincipal(r).ID)
	if err != nil {
		return nil
	}
	return off
}

// notifyKeep drops the rows of the kinds the viewer switched off.
func (a *app) notifyKeep(r *http.Request, rows []forYouRow) []forYouRow {
	// Zuerst die Zeilen: ohne sie braucht es die Schalter des Kontos nicht, und
	// waitingFor läuft bei jedem Seitenaufruf.
	if len(rows) == 0 {
		return rows
	}
	off := a.notifyOff(r)
	if len(off) == 0 {
		return rows
	}
	kept := make([]forYouRow, 0, len(rows))
	for _, f := range rows {
		if !off[notifyKindOf(f)] {
			kept = append(kept, f)
		}
	}
	return kept
}

// profileNotify is one switch of the block "Notifications".
type profileNotify struct {
	Kind, Label, Aria string
	On                bool
	// Next is the state a click posts: "0" turns the kind off, "1" on.
	Next string
}

func (a *app) profileNotifyRows(r *http.Request, l lang) []profileNotify {
	off := a.notifyOff(r)
	rows := make([]profileNotify, 0, len(store.NotifyKinds))
	for _, k := range store.NotifyKinds {
		label := l.msg(notifyKeys[k])
		next := "0"
		if off[k] {
			next = "1"
		}
		rows = append(rows, profileNotify{Kind: k, Label: label, Aria: l.msg("notify.aria", label), On: !off[k], Next: next})
	}
	return rows
}

// profileNotifySave switches one kind on or off and returns to the block.
func (a *app) profileNotifySave(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("kind")
	if !slices.Contains(store.NotifyKinds, kind) {
		http.Error(w, a.langFor(r).msg("adm.err.generic"), http.StatusBadRequest)
		return
	}
	if err := a.store.SetNotify(browserPrincipal(r).ID, kind, r.FormValue("on") == "1"); err != nil {
		http.Error(w, a.langFor(r).msg("adm.err.generic"), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/profile#benachrichtigungen", http.StatusSeeOther)
}
