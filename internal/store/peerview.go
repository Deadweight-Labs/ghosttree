package store

// SharesContextWith sagt, ob der Betrachter das Konto kennen darf: es ist sein
// eigenes, oder beide sind Mitglied derselben Organisation oder haben dasselbe
// Projekt. Für den Instanz-Admin gilt jeder als bekannt. Ein fremdes Konto soll
// nicht einmal an seinem Profilbild erkennbar sein: die Konto-Nummern zählen
// fortlaufend. Das gilt in jedem Modus, es ist der Org-Rand.
func (a *ProjectAccess) SharesContextWith(account int64) bool {
	if !a.valid || account <= 0 {
		return false
	}
	if a.account == account {
		return true
	}
	if a.IsAdmin() {
		return true
	}
	other := principalOfID(account)
	mine, _ := a.st.ListOrgs(a.principal.ID)
	theirs, _ := a.st.ListOrgs(other)
	for _, m := range mine {
		for _, t := range theirs {
			if m.ID == t.ID {
				return true
			}
		}
	}
	roles, _ := a.st.AccountRoles(other)
	for remote, r := range roles {
		if r.Any() && a.Role(remote).Any() {
			return true
		}
	}
	return false
}
