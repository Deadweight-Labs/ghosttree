package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Join-Sitzung und Paarung (REQ-434, Paket P2).
//
// Nach der Annahme einer Einladung (oder auf Wunsch eines angemeldeten Kontos)
// ist die Join-Sitzung an das Konto gebunden, und die Seite zeigt den
// Paarungscode samt Befehl. Der Installer meldet sich damit bei
// POST /api/join/claim; die Seite zeigt dann "<Gerät> wants to connect" mit
// Maschinenname, Absenderadresse und (wenn aussagekräftig) "Same network".
// Freigabe und Ablehnung kommen nur vom Konto aus einer interaktiven Sitzung
// (CSRF, gleiche Origin, Kontobestätigung, Kennung der gezeigten Anfrage).
//
// Zwei Wege zum Token. Loopback (Regelfall): die Entscheidung antwortet mit 303
// auf http://127.0.0.1:<port>/callback des Installers, bei Freigabe mit code und
// state, bei Ablehnung mit error=access_denied und state; das Token holt der
// Installer mit dem code_verifier (POST /api/join/token). Rückfall (Code): die
// Freigabe verlangt den im Terminal gezeigten Bestätigungscode, das Token kommt
// über den Geräte-Ablauf.
//
// Die Seite gehört dem angemeldeten Konto: Code und Gerät stehen nur dort, nie in
// einer URL, nie in einem Log. join.js fragt den Zustand ab und lädt die
// Seite neu, sobald er sich ändert; ohne Skripte übernimmt ein meta refresh.

// joinPairView ist die Sicht der Paarungsseite.
type joinPairView struct {
	State, Pair, Command, Machine, Remote string
	Nonce                                 string
	SameNet, ShowNet, NeedsCode           bool
	Interrupted                           bool
	Callback                              string
	Person, CSRFToken                     string
	Refresh                               int
	Message, MailIntro, MailTo            string
	CmdParts                              []cmdPart
	NeedsHTTPS                            bool
}

// cmdPart ist ein Stück des Befehls für die Anzeige. Der Umbruch soll nur an den
// Leerzeichen fallen (ein "--pair" darf nie zerreißen); allein die Adresse darf
// überall brechen. Der Text bleibt beim Kopieren derselbe Befehl.
type cmdPart struct {
	Text string
	URL  bool
	Sep  string
	Wbr  bool
}

func cmdParts(command string) []cmdPart {
	words := strings.Split(command, " ")
	parts := make([]cmdPart, len(words))
	for i, w := range words {
		parts[i] = cmdPart{Text: w, URL: strings.HasPrefix(w, "http://") || strings.HasPrefix(w, "https://"), Wbr: w == "|"}
		if i < len(words)-1 {
			parts[i].Sep = " "
		}
	}
	return parts
}

// joinBase ist die Adresse, unter der der Server von außen erreichbar ist: die
// öffentliche URL, sonst Schema und Host der Anfrage, mit X-Forwarded-* nur von
// einem vertrauten Proxy (wie sameOrigin und der Server beim Geräte-Login).
func (a *app) joinBase(r *http.Request) string {
	if a.publicOrigin != "" {
		return a.publicOrigin
	}
	scheme, host := "http", r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	proto, protoSet := singleForwardedValue(r.Header, "X-Forwarded-Proto")
	fwdHost, hostSet := singleForwardedValue(r.Header, "X-Forwarded-Host")
	if (protoSet || hostSet) && a.proxies.Trusts(r.RemoteAddr) && protoSet && hostSet &&
		(proto == "http" || proto == "https") && validForwardedHost(fwdHost) {
		scheme, host = proto, fwdHost
	}
	return scheme + "://" + host
}

// joinCommand ist der Befehl für den Installer. Er steht nur für https oder
// Loopback: ein Skript über http wäre auf dem Weg veränderbar.
func (a *app) joinCommand(r *http.Request, pair string) string {
	base := a.joinBase(r)
	u, err := url.Parse(base)
	if err != nil || pair == "" {
		return ""
	}
	loop := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !loop {
		return ""
	}
	return "curl -fsSL " + base + "/install.sh | sh -s -- --pair " + pair
}

// sameNetworkMeaningful: "Same network" sagt nur etwas, wenn der Server die
// echte Absenderadresse kennt. Hinter einer öffentlichen URL ohne benannte
// vertraute Proxys sähe jeder Absender wie der Proxy aus, die Zeile wäre immer
// "yes" und würde täuschen.
func (a *app) sameNetworkMeaningful() bool {
	return a.publicOrigin == "" || a.proxies.Configured()
}

// joinPairPage zeigt, je nach Zustand der Sitzung des Kontos, den Code mit
// Befehl, die Freigabefrage oder das Ergebnis.
func (a *app) joinPairPage(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	v := a.store.Join().View(p.ID)
	view := joinPairView{State: v.State, Pair: v.Pair, Machine: v.Machine, Remote: v.Remote, Nonce: v.Nonce,
		SameNet: v.Net != "" && v.Net == a.joinClientKey(r), ShowNet: a.sameNetworkMeaningful(), Callback: v.Callback, NeedsCode: v.Mode == store.JoinModeCode,
		Person: p.Label, CSRFToken: csrfOf(r)}
	switch v.State {
	case store.JoinWaiting:
		view.Command = a.joinCommand(r, v.Pair)
		if view.Command == "" {
			// ctx join lehnt http außerhalb von Loopback ab, ein Ersatzbefehl
			// liefe also ins Leere: die Seite sagt stattdessen, was fehlt.
			view.NeedsHTTPS = true
			break
		}
		view.CmdParts = cmdParts(view.Command)
		view.MailIntro = msg("pair.message", "")
		view.Message = msg("pair.message", view.Command)
		view.MailTo = "mailto:?subject=" + mailEscape(msg("pair.mail_subject")) + "&body=" + mailEscape(view.Message)
		view.Refresh = 10
	case store.JoinClaimed:
		view.Refresh = 20
	case store.JoinApproved:
		view.Refresh = 5
	case store.JoinNone:
		// Eine Seite, die sich selbst neu lud (w=1) und ihre Sitzung nicht mehr
		// findet, sagt es; ein erster Besuch zeigt nur den Knopf.
		view.Interrupted = r.URL.Query().Get("w") == "1"
	}
	a.joinScriptHeaders(w)
	a.joinWrite(w, "joinpair", view)
}

// mailEscape kodiert Text für die Teile einer mailto:-Adresse (Leerzeichen
// als %20, nie als +).
func mailEscape(in string) string {
	return strings.ReplaceAll(url.QueryEscape(in), "+", "%20")
}

// joinPairState meldet nur den Zustand der Sitzung dieses Kontos, damit die
// Seite sich weiterschaltet, sobald der Server das Gerät sieht. Sie sagt nichts,
// was die Seite nicht ohnehin zeigt; Zugang haben nur Sitzung und Konto.
func (a *app) joinPairState(w http.ResponseWriter, r *http.Request) {
	v := a.store.Join().View(browserPrincipal(r).ID)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(map[string]string{"state": v.State, "nonce": v.Nonce})
}

// joinPairMessage ist joinMessage mit dem Weg zurück zur Paarungsseite: wer dort
// einen Fehler bekommt, steht sonst vor einer Sackgasse.
func (a *app) joinPairMessage(w http.ResponseWriter, status int, title, message string) {
	a.joinMessageBack(w, status, title, message, "/join/pair", msg("join.msg_back"))
}

// joinPairCreate legt die Sitzung an oder ersetzt sie durch einen neuen Code.
// Gibt es noch keine Sitzung, bestätigt das Konto seinen Namen (wie beim
// Beitritt); ein neuer Code für eine bestehende braucht das nicht.
func (a *app) joinPairCreate(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	if a.store.Join().View(p.ID).State == store.JoinNone && r.FormValue("confirm_account") != p.Label {
		a.joinPairMessage(w, http.StatusBadRequest, msg("join.msg_confirm_t"), msg("join.msg_confirm_p"))
		return
	}
	if _, err := a.store.Join().Create(p.ID); err != nil {
		a.joinPairMessage(w, http.StatusInternalServerError, msg("join.msg_setup_t"), msg("join.msg_setup"))
		return
	}
	http.Redirect(w, r, "/join/pair", http.StatusSeeOther)
}

// joinPairDecide gibt das gemeldete Gerät frei oder lehnt es ab. Die Anfrage
// trägt die Kennung des angezeigten Geräts; gehört sie zu einem anderen, ändert
// sich nichts. Beim Loopback-Weg geht der Browser nach der Freigabe auf den
// Loopback des Installers.
func (a *app) joinPairDecide(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	approve := r.FormValue("decision") == "approve"
	if approve && r.FormValue("confirm_account") != p.Label {
		a.joinPairMessage(w, http.StatusBadRequest, msg("join.msg_confirm_t"), msg("join.msg_confirm_p"))
		return
	}
	check := func(machine string) error { return a.store.MachineClaimable(machine, p.ID) }
	dec, err := a.store.Join().Decide(p.ID, approve, r.FormValue("nonce"), r.FormValue("confirm_code"), check)
	switch {
	case err == nil && dec.Redirect != "":
		http.Redirect(w, r, dec.Redirect, http.StatusSeeOther)
	case err == nil:
		http.Redirect(w, r, "/join/pair", http.StatusSeeOther)
	case errors.Is(err, store.ErrJoinConfirm):
		a.joinPairMessage(w, http.StatusBadRequest, msg("join.msg_badcode_t"), msg("join.msg_badcode"))
	case errors.Is(err, store.ErrMachineTaken):
		a.joinPairMessage(w, http.StatusConflict, msg("join.msg_taken_t"), msg("join.msg_taken"))
	case errors.Is(err, store.ErrJoinNotReady):
		a.joinPairMessage(w, http.StatusConflict, msg("join.msg_nothing_t"), msg("join.msg_nothing"))
	default:
		a.joinPairMessage(w, http.StatusInternalServerError, msg("join.msg_setup_t"), msg("join.msg_setup"))
	}
}
