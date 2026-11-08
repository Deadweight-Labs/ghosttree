package store

import (
	"sort"
	"strconv"
)

// Stufen, in denen ein Konto eine Maschine und ihr Wissen erreicht.
const (
	levelNone   = 0
	levelRead   = 1
	levelWrite  = 2
	levelManage = 3
)

type machineRec struct{ account, org int64 }

type machineGrant struct {
	org  int64
	role string
}

type entryShareKey struct {
	kind string
	id   int64
}

type entryShareRow struct {
	org  int64
	host string
}

// machineIndex ist, was die Zugriffsprüfung einer Anfrage über Maschinen
// braucht: wem welche gehört, an wen sie freigegeben ist, welche Einträge
// freigegeben sind und in welchen Orgs der Betrachter Mitglied ist. Alle vier
// Tabellen sind klein (Maschinen, Freigaben), deshalb wird einmal je Anfrage
// gelesen.
type machineIndex struct {
	recs    map[string]machineRec
	grants  map[string][]machineGrant
	entries map[entryShareKey][]entryShareRow
	member  map[int64]string
}

func (s *Store) machineAccessIndex(account int64) *machineIndex {
	if s.reader != nil {
		return s.reader.machineAccessIndex(account)
	}
	ix := &machineIndex{
		recs: map[string]machineRec{}, grants: map[string][]machineGrant{},
		entries: map[entryShareKey][]entryShareRow{}, member: map[int64]string{},
	}
	owner := instanceOwnerID(s.db)
	if rows, err := s.db.Query(`SELECT hostname, account_id, owner_org_id FROM machines`); err == nil {
		for rows.Next() {
			var host string
			var id, org int64
			if rows.Scan(&host, &id, &org) == nil {
				ix.recs[canonicalMachine(host)] = machineRec{account: effectiveOwner(id, owner), org: org}
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT hostname, org_id, role FROM machine_shares`); err == nil {
		for rows.Next() {
			var host, role string
			var org int64
			if rows.Scan(&host, &org, &role) == nil {
				ix.grants[canonicalMachine(host)] = append(ix.grants[canonicalMachine(host)], machineGrant{org: org, role: role})
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT kind, entry_id, org_id, hostname FROM machine_entry_shares`); err == nil {
		for rows.Next() {
			var kind, host string
			var id, org int64
			if rows.Scan(&kind, &id, &org, &host) == nil {
				k := entryShareKey{kind, id}
				ix.entries[k] = append(ix.entries[k], entryShareRow{org: org, host: canonicalMachine(host)})
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT om.org_id, om.role FROM org_members om WHERE om.account_id=?
		AND NOT EXISTS(SELECT 1 FROM org_guests g WHERE g.org_id=om.org_id AND g.account_id=om.account_id)`, account); err == nil {
		for rows.Next() {
			var org int64
			var role string
			if rows.Scan(&org, &role) == nil {
				ix.member[org] = role
			}
		}
		rows.Close()
	}
	return ix
}

// grantRole: die Rolle, mit der die Org auf die Maschine zugreift, leer ohne
// Freigabe. Die besitzende Org einer Org-Maschine liest ihre Einträge immer.
func (ix *machineIndex) grantRole(machine string, org int64) string {
	if rec, ok := ix.recs[machine]; ok && rec.org == org {
		return ShareRead
	}
	for _, g := range ix.grants[machine] {
		if g.org == org {
			return g.role
		}
	}
	return ""
}

// sharedOrgs nennt die Orgs, an die der Eintrag freigegeben ist, soweit die
// Freigabe der Maschine noch besteht und der Eintrag noch auf der Maschine
// liegt, für die er freigegeben wurde.
func (ix *machineIndex) sharedOrgs(kind string, id int64, machine string) []int64 {
	machine = canonicalMachine(machine)
	var out []int64
	for _, row := range ix.entries[entryShareKey{kind, id}] {
		if row.host == machine && ix.grantRole(machine, row.org) != "" {
			out = append(out, row.org)
		}
	}
	return out
}

func (a *ProjectAccess) loadMachines() {
	a.machOnce.Do(func() {
		a.machines, a.owner = a.st.machineOwners()
	})
}

// index lädt die Freigaben erst, wenn eine Entscheidung sie braucht: die
// meisten Anfragen fragen nie nach einer Maschine.
func (a *ProjectAccess) index() *machineIndex {
	a.miOnce.Do(func() {
		if a.mi == nil {
			a.mi = a.st.machineAccessIndex(a.account)
		}
	})
	return a.mi
}

// machineLevel: wie weit das Konto eine Maschine und, über shared (die Orgs, an
// die der Eintrag freigegeben ist), einen Eintrag darauf erreicht.
//
//	manage: Besitzer, bei Org-Maschinen ein Maintainer (Owner der Org). Sieht
//	        und pflegt alles auf der Maschine.
//	write:  Mitglied einer Org mit Schreib-Freigabe, der Eintrag ist an sie freigegeben.
//	read:   Mitglied einer Org mit Freigabe, der Eintrag ist an sie freigegeben.
//
// Ohne ausdrückliche Freigabe des Eintrags bleibt es bei none, auch für ein
// Mitglied: eine geteilte Maschine macht ihr Wissen nicht von selbst geteilt.
// Gäste der Org zählen nie als Mitglied.
func (a *ProjectAccess) machineLevel(machine string, shared []int64) int {
	if !a.valid {
		return levelNone
	}
	a.loadMachines()
	ix := a.index()
	name := canonicalMachine(machine)
	rec, known := ix.recs[name]
	if !known {
		// Eine Maschine, die nie jemand beansprucht hat, gehört wie Altbestand
		// dem Instanz-Owner.
		if a.owner == a.account {
			return levelManage
		}
		return levelNone
	}
	if rec.org > 0 {
		if ix.member[rec.org] == OrgOwner {
			return levelManage
		}
	} else if rec.account == a.account {
		return levelManage
	}
	best := levelNone
	for _, org := range shared {
		if _, member := ix.member[org]; !member {
			continue
		}
		switch ix.grantRole(name, org) {
		case ShareWrite:
			best = max(best, levelWrite)
		case ShareRead:
			best = max(best, levelRead)
		}
	}
	return best
}

// orgRoleOf ist die Rolle eines beliebigen Kontos in einer Org (ohne Gäste),
// einmal je Anfrage und Paar gelesen.
func (a *ProjectAccess) orgRoleOf(account, org int64) string {
	key := strconv.FormatInt(account, 10) + "/" + strconv.FormatInt(org, 10)
	if v, ok := a.orgRoles[key]; ok {
		return v
	}
	v := orgMemberRoleTx(a.st.db, org, account)
	if a.orgRoles == nil {
		a.orgRoles = map[string]string{}
	}
	a.orgRoles[key] = v
	return v
}

// authorEntitled: der Autor darf die Maschine jetzt verantworten: er besitzt
// sie (bei Org-Maschinen: Maintainer der Org), oder er ist Mitglied einer Org
// mit Schreib-Freigabe, an die der Eintrag freigegeben ist. Ohne diese Bindung
// ließe sich Text auf einen freien Maschinennamen legen, den der spätere
// Besitzer dann als bindende Anweisung bekäme (#2581).
func (a *ProjectAccess) authorEntitled(machine string, author int64, shared []int64) bool {
	a.loadMachines()
	ix := a.index()
	name := canonicalMachine(machine)
	rec, known := ix.recs[name]
	if !known {
		return author != 0 && author == a.owner
	}
	if rec.org > 0 {
		if a.orgRoleOf(author, rec.org) == OrgOwner {
			return true
		}
	} else if rec.account == author {
		return true
	}
	for _, org := range shared {
		if a.authorWritesVia(name, author, org) {
			return true
		}
	}
	return false
}

// authorWritesVia: der Autor darf über genau diese Org auf der Maschine
// schreiben: Schreib-Freigabe der Maschine an diese Org und er ist dort Mitglied
// (kein Gast). Eine Schreib-Freigabe an eine andere Org zählt nicht.
func (a *ProjectAccess) authorWritesVia(machine string, author, org int64) bool {
	return a.index().grantRole(canonicalMachine(machine), org) == ShareWrite && a.orgRoleOf(author, org) != ""
}

// entitledSharedOrgs: die Orgs, an die der Eintrag wirksam freigegeben ist und
// über die sein Autor die Maschine verantworten darf. Ein Verwalter der Maschine
// als Autor gilt für jede freigegebene Org.
func (a *ProjectAccess) entitledSharedOrgs(machine string, author int64, shared []int64) []int64 {
	if len(shared) == 0 {
		return nil
	}
	a.loadMachines()
	if author == 0 {
		author = a.owner
	}
	if a.authorEntitled(machine, author, nil) {
		return shared
	}
	var out []int64
	for _, org := range shared {
		if a.authorWritesVia(machine, author, org) {
			out = append(out, org)
		}
	}
	return out
}

// mayShareOnCreate: darf der Schreiber beim Anlegen an diese Org freigeben?
// Ein Verwalter der Maschine an jede Org, für die sie freigegeben ist. Sonst nur
// an Orgs, in denen er Mitglied ist und deren Maschinenfreigabe write ist.
func (a *ProjectAccess) mayShareOnCreate(machine string, org int64) bool {
	if a.machineLevel(machine, nil) == levelManage {
		return a.index().grantRole(canonicalMachine(machine), org) != ""
	}
	return a.authorWritesVia(machine, a.account, org)
}

// shareAllows: was ein Mitglied über eine Freigabe tun darf. Lesen und, mit
// Schreib-Freigabe, eigene Einträge anlegen und pflegen. Ein fremder Eintrag
// bleibt unveränderlich, sonst stünde neuer Text unter dem Autor des alten und
// würde als dessen Anweisung ausgeliefert. Verifizieren, Freigeben und alles
// an Abläufen außer Lesen bleibt dem Besitzer.
func shareAllows(level int, res Resource, act Action, obj Object) bool {
	switch act {
	case ActRead:
		// Entwürfe und Quarantäne bleiben beim Besitzer.
		switch obj.Confidence {
		case "", "trusted", "verified":
			return true
		}
		return false
	case ActCreate:
		return level >= levelWrite && res == ResKnowledge
	case ActEdit:
		return level >= levelWrite && res == ResKnowledge && obj.Own
	}
	return false
}

// withoutOrgs: list ohne die Orgs aus drop.
func withoutOrgs(list, drop []int64) []int64 {
	if len(drop) == 0 {
		return list
	}
	var out []int64
	for _, o := range list {
		if !containsOrg(drop, o) {
			out = append(out, o)
		}
	}
	return out
}

func containsOrg(list []int64, org int64) bool {
	for _, o := range list {
		if o == org {
			return true
		}
	}
	return false
}

// sharedOrgsOf: die Orgs, an die ein Eintrag mit Maschinen-Scope jetzt
// wirksam freigegeben ist (Freigabe der Maschine besteht noch).
func (a *ProjectAccess) sharedOrgsOf(kind string, id int64, machine string) []int64 {
	if id == 0 || machine == "" {
		return nil
	}
	return a.index().sharedOrgs(kind, id, machine)
}

// ---------------------------------------------------------------- Lesen

func (s *Store) orgSlugs() map[int64]string {
	out := map[int64]string{}
	rows, err := s.db.Query(`SELECT id, slug FROM orgs`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var slug string
		if rows.Scan(&id, &slug) == nil {
			out[id] = slug
		}
	}
	return out
}

// Beziehung des Betrachters zu einer Maschine (Machine.Relation).
const (
	RelOwner       = "owner"
	RelMaintainer  = "maintainer"
	RelMember      = "member"
	RelSharedRead  = "shared-read"
	RelSharedWrite = "shared-write"
)

// MachinesFor liefert die Maschinen, die das Konto etwas angehen: eigene, die
// einer Org mit seiner Maintainer- oder Mitgliedsrolle und die, die eine seiner
// Orgs freigegeben bekam. Besitzer sehen alle Freigaben, Mitglieder nur die
// ihrer eigenen Orgs. Alles andere gibt es für das Konto nicht.
func (s *Store) MachinesFor(accountPrincipal string) ([]Machine, error) {
	if s.reader != nil {
		return s.reader.MachinesFor(accountPrincipal)
	}
	account, ok := accountNumericID(accountPrincipal)
	if !ok {
		return []Machine{}, nil
	}
	return s.machinesFor(account, "")
}

// MachineFor ist die Seite einer Maschine für ein Konto, mit Protokoll für
// Verwalter. Wer sie nicht erreicht, bekommt ErrMachineNotFound, genau wie bei
// einer Maschine, die es nicht gibt.
func (s *Store) MachineFor(accountPrincipal, name string) (Machine, error) {
	if s.reader != nil {
		return s.reader.MachineFor(accountPrincipal, name)
	}
	account, ok := accountNumericID(accountPrincipal)
	if !ok {
		return Machine{}, ErrMachineNotFound
	}
	list, err := s.machinesFor(account, canonicalMachine(name))
	if err != nil {
		return Machine{}, err
	}
	if len(list) == 0 {
		return Machine{}, ErrMachineNotFound
	}
	m := list[0]
	if m.Relation == RelOwner || m.Relation == RelMaintainer {
		m.Events = s.machineEvents(canonicalMachine(name))
	}
	return m, nil
}

func (s *Store) machineEvents(host string) []MachineEvent {
	names, _ := s.accountNames()
	rows, err := s.db.Query(`SELECT action, actor, subject, detail, via, created_at FROM machine_events WHERE hostname=? ORDER BY id DESC LIMIT 50`, host)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []MachineEvent
	for rows.Next() {
		var e MachineEvent
		var actor int64
		if rows.Scan(&e.Action, &actor, &e.Subject, &e.Detail, &e.Via, &e.CreatedAt) == nil {
			e.Actor = names[actor]
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) machinesFor(account int64, only string) ([]Machine, error) {
	ix := s.machineAccessIndex(account)
	names, err := s.accountNames()
	if err != nil {
		return nil, err
	}
	slugs := s.orgSlugs()
	rows, err := s.db.Query(`SELECT hostname, first_seen, last_seen FROM machines ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	type seen struct{ host, first, last string }
	var all []seen
	for rows.Next() {
		var m seen
		if err := rows.Scan(&m.host, &m.first, &m.last); err != nil {
			rows.Close()
			return nil, err
		}
		if only == "" || canonicalMachine(m.host) == only {
			all = append(all, m)
		}
	}
	rows.Close()
	grantors := s.shareGrantors()
	out := []Machine{}
	for _, m := range all {
		name := canonicalMachine(m.host)
		rec := ix.recs[name]
		rel, level := "", levelNone
		switch {
		case rec.org > 0 && ix.member[rec.org] == OrgOwner:
			rel, level = RelMaintainer, levelManage
		case rec.org == 0 && rec.account == account:
			rel, level = RelOwner, levelManage
		case rec.org > 0 && ix.member[rec.org] != "":
			rel = RelMember
		default:
			for _, g := range ix.grants[name] {
				if _, member := ix.member[g.org]; !member {
					continue
				}
				if g.role == ShareWrite {
					rel = RelSharedWrite
				} else if rel != RelSharedWrite {
					rel = RelSharedRead
				}
			}
		}
		if rel == "" {
			continue
		}
		v := Machine{
			Name: name, AccountID: "person:" + strconv.FormatInt(rec.account, 10), Owner: names[rec.account],
			FirstSeen: m.first, LastSeen: m.last, OwnerOrg: slugs[rec.org], Relation: rel,
		}
		for _, g := range s.machineGrantRows(name, slugs, grantors, names) {
			if level == levelManage || ix.member[g.OrgID] != "" {
				v.Shares = append(v.Shares, g)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// shareGrantors liefert je (Maschine, Org) das Konto, das die Freigabe erteilte.
func (s *Store) shareGrantors() map[string]int64 {
	out := map[string]int64{}
	rows, err := s.db.Query(`SELECT hostname, org_id, granted_by FROM machine_shares`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var host string
		var org, by int64
		if rows.Scan(&host, &org, &by) == nil {
			out[canonicalMachine(host)+"/"+strconv.FormatInt(org, 10)] = by
		}
	}
	return out
}

func (s *Store) machineGrantRows(host string, slugs map[int64]string, grantors map[string]int64, names map[int64]string) []MachineShare {
	rows, err := s.db.Query(`SELECT org_id, role, os_user, path_prefix, created_at FROM machine_shares WHERE hostname=?`, host)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []MachineShare
	for rows.Next() {
		var g MachineShare
		if rows.Scan(&g.OrgID, &g.Role, &g.OSUser, &g.PathPrefix, &g.CreatedAt) == nil {
			g.Org = slugs[g.OrgID]
			g.GrantedBy = names[grantors[host+"/"+strconv.FormatInt(g.OrgID, 10)]]
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Org < out[j].Org })
	return out
}

// EntryShareOrgs nennt die Orgs, an die ein Eintrag freigegeben ist. Der
// Verwalter der Maschine sieht alle, jeder andere nur Orgs, in denen er selbst
// Mitglied ist.
func (s *Store) EntryShareOrgs(viewer Principal, kind string, id int64) []string {
	if s.reader != nil {
		return s.reader.EntryShareOrgs(viewer, kind, id)
	}
	account, ok := accountNumericID(viewer.ID)
	if !ok {
		return nil
	}
	var host, project string
	switch kind {
	case EntryKnowledge:
		_ = s.db.QueryRow(`SELECT machine, project FROM knowledge WHERE id=?`, id).Scan(&host, &project)
	case EntryRunbook:
		_ = s.db.QueryRow(`SELECT machine, project FROM runbooks WHERE id=?`, id).Scan(&host, &project)
	}
	// Ein Eintrag mit Projekt wird über das Projekt gesehen, nicht über Freigaben.
	if host == "" || project != "" {
		return nil
	}
	ix := s.machineAccessIndex(account)
	a := &ProjectAccess{st: s, principal: viewer, account: account, valid: true, mi: ix}
	manage := a.machineLevel(host, nil) == levelManage
	slugs := s.orgSlugs()
	var out []string
	for _, org := range ix.sharedOrgs(kind, id, host) {
		if _, member := ix.member[org]; manage || member {
			out = append(out, slugs[org])
		}
	}
	sort.Strings(out)
	return out
}

// machineEntryLimit begrenzt, wie viele Einträge eine Maschinenseite je Art
// nennt: die Seite ist ein Überblick, die Suche (context_search machine=...)
// findet den Rest.
const machineEntryLimit = 100

// MachineRefs liefert die aktiven Wissenseinträge und die Abläufe, die diese
// Maschine am Eintrag tragen, neueste zuerst, ohne Zugriffsprüfung: wer sie
// sehen darf, entscheidet ProjectAccess.MachineEntries.
func (s *Store) MachineRefs(host string) ([]Knowledge, []RunbookRef, error) {
	if s.reader != nil {
		return s.reader.MachineRefs(host)
	}
	host = canonicalMachine(host)
	rows, err := s.db.Query(`SELECT `+knowledgeCols+` FROM knowledge WHERE machine=? AND status='active' ORDER BY id DESC`, host)
	if err != nil {
		return nil, nil, err
	}
	ks, err := s.scanKnowledge(rows)
	if err != nil {
		return nil, nil, err
	}
	rrows, err := s.db.Query(`SELECT `+runbookRefCols+` FROM runbooks WHERE machine=? ORDER BY id DESC`, host)
	if err != nil {
		return nil, nil, err
	}
	defer rrows.Close()
	var refs []RunbookRef
	for rrows.Next() {
		r, err := runbookRefScan(rrows)
		if err != nil {
			return nil, nil, err
		}
		refs = append(refs, r)
	}
	return ks, refs, rrows.Err()
}

// MachineEntries nennt die Einträge der Maschinenseite, die der Fragende lesen
// darf. Ein Eintrag mit Projekt erscheint nur, wenn die Projektrolle ihn
// freigibt (dieselbe Prüfung wie jede Liste), nie wegen der Maschine allein;
// ein Eintrag ohne Projekt nur dem Besitzer der Maschine und, wenn
// freigegeben, den Mitgliedern der Org. Gerufen wird es für eine Maschine, die
// der Fragende erreicht (MachineFor).
func (a *ProjectAccess) MachineEntries(host string) ([]MachineEntry, error) {
	ks, refs, err := a.st.MachineRefs(host)
	if err != nil {
		return nil, err
	}
	a.Filtered()
	var out []MachineEntry
	nk := 0
	for _, k := range ks {
		if nk >= machineEntryLimit {
			break
		}
		if !a.CanSeeKnowledge(k) {
			continue
		}
		out = append(out, MachineEntry{Kind: EntryKnowledge, ID: k.ID, Title: k.Title, Type: k.Type, Project: k.Scope.Project})
		nk++
	}
	nr := 0
	for _, r := range refs {
		if nr >= machineEntryLimit {
			break
		}
		if !a.CanSeeRunbook(r) {
			continue
		}
		out = append(out, MachineEntry{Kind: EntryRunbook, ID: r.ID, Title: r.Name, Project: r.Project})
		nr++
	}
	return out, nil
}
