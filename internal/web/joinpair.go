package web

import (
	"errors"
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Join-Sitzung und Paarung (REQ-434, Paket P2).
//
// Nach der Annahme einer Einladung (oder auf Wunsch eines angemeldeten Kontos)
// legt der Server eine Join-Sitzung an und zeigt den Paarungscode samt Befehl.
// Der Installer meldet sich damit bei POST /api/join/claim; die Seite zeigt dann
// "<Gerät> wants to connect" mit dem Code zum Abgleich. Erst die Freigabe des
// Kontos aus einer interaktiven Sitzung (CSRF, gleiche Origin, Kontobestätigung)
// lässt den Geräte-Ablauf das Token ausstellen.
//
// Die Seite gehört dem angemeldeten Konto: Code und Gerät stehen nur dort, nie in
// einer URL, nie in einem Log. Sie lädt sich selbst neu (meta refresh), solange
// sie wartet; das CSP der Join-Seiten erlaubt keine Skripte.

// joinPairView ist die Sicht der Paarungsseite.
type joinPairView struct {
	State, Pair, Command, Machine, Remote string
	Conflict, Interrupted                 bool
	Person, AccountID, CSRFToken          string
	Refresh                               int
}

func (a *app) joinBase(r *http.Request) string {
	if a.publicOrigin != "" {
		return a.publicOrigin
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// joinPairPage zeigt, je nach Zustand der Sitzung des Kontos, den Code mit
// Befehl, die Freigabefrage oder das Ergebnis.
func (a *app) joinPairPage(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	v := a.store.Join().View(p.ID)
	view := joinPairView{State: v.State, Pair: v.Pair, Machine: v.Machine, Remote: v.Remote, Conflict: v.Conflicts > 0,
		Person: p.Label, AccountID: p.ID, CSRFToken: csrfOf(r)}
	switch v.State {
	case store.JoinWaiting:
		view.Command = "curl -fsSL " + a.joinBase(r) + "/install.sh | sh -s -- --pair " + v.Pair
		view.Refresh = 5
	case store.JoinApproved:
		view.Refresh = 3
	case store.JoinNone:
		// Eine Seite, die sich selbst neu lud (w=1) und ihre Sitzung nicht mehr
		// findet, sagt es; ein erster Besuch zeigt nur den Knopf.
		view.Interrupted = r.URL.Query().Get("w") == "1"
	}
	a.joinHeaders(w)
	a.joinWrite(w, "joinpair", view)
}

// joinPairCreate legt die Sitzung an oder ersetzt sie durch einen neuen Code.
// Gibt es noch keine Sitzung, bestätigt das Konto seinen Namen (wie beim
// Beitritt); ein neuer Code für eine bestehende braucht das nicht.
func (a *app) joinPairCreate(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	if a.store.Join().View(p.ID).State == store.JoinNone && r.FormValue("confirm_account") != p.Label {
		a.joinMessage(w, http.StatusBadRequest, "Please confirm your account", "Tick the box that names the account this machine will connect to.")
		return
	}
	if _, err := a.store.Join().Create(p.ID); err != nil {
		a.joinMessage(w, http.StatusInternalServerError, "Setup failed", "Something went wrong. Try again in a moment.")
		return
	}
	http.Redirect(w, r, "/join/pair", http.StatusSeeOther)
}

// joinPairDecide gibt das gemeldete Gerät frei oder lehnt es ab.
func (a *app) joinPairDecide(w http.ResponseWriter, r *http.Request) {
	p := browserPrincipal(r)
	approve := r.FormValue("decision") == "approve"
	if approve {
		if r.FormValue("confirm_account") != p.Label {
			a.joinMessage(w, http.StatusBadRequest, "Please confirm your account", "Go back and tick the box that names the account this machine will connect to.")
			return
		}
		if err := a.store.MachineClaimable(a.store.Join().View(p.ID).Machine, p.ID); err != nil {
			a.joinMessage(w, http.StatusConflict, "Machine name taken", "That machine name belongs to another account. Run the command again with a different machine name.")
			return
		}
	}
	switch err := a.store.Join().Decide(p.ID, approve); {
	case err == nil:
		http.Redirect(w, r, "/join/pair", http.StatusSeeOther)
	case errors.Is(err, store.ErrJoinNotReady):
		a.joinMessage(w, http.StatusConflict, "Nothing to approve", "No machine is waiting for this account.")
	default:
		a.joinMessage(w, http.StatusInternalServerError, "Setup failed", "Something went wrong. Try again in a moment.")
	}
}
