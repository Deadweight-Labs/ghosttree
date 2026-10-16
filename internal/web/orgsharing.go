package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// WithSourceProver schaltet "GitHub verbinden" ein. Ohne ihn, oder solange die
// Zugangsdaten auf dem Server fehlen, zeigt die Seite "Nicht eingerichtet".
func WithSourceProver(p forge.Prover) Option { return func(a *app) { a.prover = p } }

const (
	sourceFlowTTL    = 10 * time.Minute
	maxSourceFlows   = 200
	sourcePathPrefix = "/ui/orgs/source/"
)

type sourceFlow struct {
	account string
	org     string
	until   time.Time
	// accounts ist gesetzt, sobald der Nachweis geliefert wurde.
	accounts []forge.Account
}

// sourceFlows hält die offenen Verbindungsabläufe im Speicher, nur für den
// Konto-Inhaber, der sie begonnen hat. Das Zugriffstoken von GitHub wird nie
// gehalten: es lebt nur während des Aufrufs in forge.Prover.Prove.
type sourceFlows struct {
	mu    sync.Mutex
	flows map[string]sourceFlow
	now   func() time.Time
}

func newSourceFlows() *sourceFlows {
	return &sourceFlows{flows: map[string]sourceFlow{}, now: time.Now}
}

func randomToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (f *sourceFlows) start(account, org string) (string, error) {
	tok, err := randomToken()
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneLocked()
	if len(f.flows) >= maxSourceFlows {
		f.flows = map[string]sourceFlow{}
	}
	f.flows[tok] = sourceFlow{account: account, org: org, until: f.now().Add(sourceFlowTTL)}
	return tok, nil
}

// take löst einen Ablauf ein und verbraucht ihn.
func (f *sourceFlows) take(tok, account string) (sourceFlow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.flows[tok]
	if !ok || f.now().After(fl.until) || fl.account != account {
		return sourceFlow{}, false
	}
	delete(f.flows, tok)
	return fl, true
}

func (f *sourceFlows) get(tok, account string) (sourceFlow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.flows[tok]
	if !ok || f.now().After(fl.until) || fl.account != account {
		return sourceFlow{}, false
	}
	return fl, true
}

func (f *sourceFlows) put(tok string, fl sourceFlow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flows[tok] = fl
}

func (f *sourceFlows) pruneLocked() {
	now := f.now()
	for k, v := range f.flows {
		if now.After(v.until) {
			delete(f.flows, k)
		}
	}
}

// sourceRow ist eine verbundene Quelle mit dem Präfix, das sie belegt.
type sourceRow struct {
	store.Source
	Prefix store.OrgPrefix
	Shared bool
	// Toggle ist die Sichtbarkeit, auf die der Schalter das Präfix stellt.
	Toggle string
}

// sourcePick sind die GitHub-Organisationen, in denen der Mensch Admin ist und
// die er jetzt verbinden kann.
type sourcePick struct {
	Token    string
	Accounts []string
}

func (a *app) sourceReady() bool { return a.prover != nil && a.prover.Configured() }

func (a *app) callbackURL(r *http.Request) string {
	return a.joinBase(r) + sourcePathPrefix + "callback"
}

// orgProjectVisibility schaltet ein Projekt zwischen "mit der Org geteilt" und
// "privat". Die Regeln prüft der Store.
func (a *app) orgProjectVisibility(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		p, known := a.store.ProjectByRemote(r.FormValue("remote"))
		if !known || p.OrgID != o.ID {
			err = store.ErrProjectNotFound
		} else {
			err = a.store.SetProjectVisibility(browserPrincipal(r).ID, p.Remote, r.FormValue("shared") == "1")
		}
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "visibility")
}

// orgSourceConnect schickt den Browser des Owners zu GitHub, wo er die App für
// seine Organisation bestätigt.
func (a *app) orgSourceConnect(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil && a.store.OrgRole(o.ID, browserPrincipal(r).ID) != store.OrgOwner {
		err = store.ErrNotOrgOwner
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	if !a.sourceReady() {
		a.orgFailure(w, r, forge.ErrNotConfigured)
		return
	}
	state, err := a.sourceFlows.start(browserPrincipal(r).ID, o.Slug)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	http.Redirect(w, r, a.prover.AuthorizeURL(state, a.callbackURL(r)), http.StatusSeeOther)
}

// orgSourceCallback ist die Rückkehr von GitHub: der state bindet sie an den
// Browser, der sie begonnen hat. Der Nachweis (Admin welcher GitHub-Organisation)
// wird geholt und für den nächsten Schritt kurz gemerkt; das Token ist dann
// schon weg.
func (a *app) orgSourceCallback(w http.ResponseWriter, r *http.Request) {
	me := browserPrincipal(r).ID
	fl, ok := a.sourceFlows.take(r.URL.Query().Get("state"), me)
	if !ok {
		http.Redirect(w, r, "/ui/orgs?notice=source_failed", http.StatusSeeOther)
		return
	}
	if !a.sourceReady() || r.URL.Query().Get("code") == "" {
		orgRedirect(w, r, fl.org, "source_failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	accounts, err := a.prover.Prove(ctx, r.URL.Query().Get("code"), a.callbackURL(r))
	if err != nil {
		orgRedirect(w, r, fl.org, "source_failed")
		return
	}
	pick, err := a.sourceFlows.start(me, fl.org)
	if err != nil {
		orgRedirect(w, r, fl.org, "source_failed")
		return
	}
	fl.accounts, fl.until = accounts, a.sourceFlows.now().Add(sourceFlowTTL)
	a.sourceFlows.put(pick, fl)
	http.Redirect(w, r, "/ui/orgs?org="+url.QueryEscape(fl.org)+"&pick="+pick, http.StatusSeeOther)
}

// orgSourceAdd verbindet eine der nachgewiesenen GitHub-Organisationen und
// beansprucht ihr Präfix mit der gewählten Sichtbarkeit für neue Projekte.
func (a *app) orgSourceAdd(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	me := browserPrincipal(r).ID
	fl, ok := a.sourceFlows.get(r.FormValue("pick"), me)
	login := strings.ToLower(strings.TrimSpace(r.FormValue("login")))
	if !ok || fl.org != o.Slug {
		orgRedirect(w, r, o.Slug, "source_failed")
		return
	}
	var proven *forge.Account
	for i := range fl.accounts {
		if fl.accounts[i].Login == login {
			proven = &fl.accounts[i]
		}
	}
	if proven == nil {
		orgRedirect(w, r, o.Slug, "source_failed")
		return
	}
	visibility := store.VisibilityPrivate
	if r.FormValue("visibility") == store.VisibilityShared {
		visibility = store.VisibilityShared
	}
	if _, err := a.store.ConnectSource(me, o.ID, store.SourceGitHub, proven.Login, proven.Repos); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	if _, err := a.store.ClaimPrefix(me, o.ID, "github.com/"+proven.Login+"/*", visibility); err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "source_added")
}

func (a *app) orgSourceRemove(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		err = a.store.DisconnectSource(browserPrincipal(r).ID, o.ID, store.SourceGitHub, r.FormValue("login"))
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "source_removed")
}

func (a *app) orgSourceImport(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		var id int64
		for _, s := range a.sourceListFor(browserPrincipal(r).ID, o.ID) {
			if s.Login == strings.ToLower(strings.TrimSpace(r.FormValue("login"))) {
				id = s.ID
			}
		}
		_, err = a.store.ImportSourceRepos(browserPrincipal(r).ID, o.ID, id)
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "imported")
}

// orgPrefixVisibility ändert, was ein Präfix neuen Projekten als Sichtbarkeit mitgibt.
func (a *app) orgPrefixVisibility(w http.ResponseWriter, r *http.Request) {
	o, err := a.formOrg(r)
	if err == nil {
		_, err = a.store.ClaimPrefix(browserPrincipal(r).ID, o.ID, r.FormValue("prefix"), r.FormValue("visibility"))
	}
	if err != nil {
		a.orgFailure(w, r, err)
		return
	}
	orgRedirect(w, r, o.Slug, "visibility")
}

func (a *app) sourceListFor(me string, orgID int64) []store.Source {
	list, _ := a.store.ListSources(me, orgID)
	return list
}

// fillSources sammelt die verbundenen Quellen und, nach der Rückkehr von GitHub,
// die zur Wahl stehenden Organisationen.
func (a *app) fillSources(r *http.Request, v *orgsView, me string) {
	v.SourceReady = a.sourceReady()
	prefixes, _ := a.store.ListOrgPrefixes(me, v.Selected.ID)
	byPrefix := map[string]store.OrgPrefix{}
	for _, p := range prefixes {
		byPrefix[p.Prefix] = p
	}
	connected := map[string]bool{}
	for _, s := range a.sourceListFor(me, v.Selected.ID) {
		connected[s.Login] = true
		row := sourceRow{Source: s}
		if p, ok := byPrefix["github.com/"+s.Login]; ok {
			row.Prefix, row.Shared = p, p.Visibility == store.VisibilityShared
			row.Toggle = map[bool]string{true: store.VisibilityPrivate, false: store.VisibilityShared}[row.Shared]
		}
		v.Sources = append(v.Sources, row)
	}
	if tok := r.URL.Query().Get("pick"); tok != "" && v.Owner {
		if fl, ok := a.sourceFlows.get(tok, me); ok && fl.org == v.Selected.Slug {
			pick := &sourcePick{Token: tok, Accounts: []string{}}
			for _, acct := range fl.accounts {
				if !connected[acct.Login] {
					pick.Accounts = append(pick.Accounts, acct.Login)
				}
			}
			v.Pick = pick
		}
	}
}

func sourceError(err error) bool { return errors.Is(err, forge.ErrNotConfigured) }
