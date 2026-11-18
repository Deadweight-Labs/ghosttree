package store

import "testing"

func TestTokenIssueAndRevokeLeaveAnAppendOnlyTrail(t *testing.T) {
	st := openTest(t)
	if _, err := st.AddPerson("alice"); err != nil {
		t.Fatal(err)
	}
	acct, err := st.AccountByName("alice")
	if err != nil {
		t.Fatal(err)
	}
	_, info, err := st.CreateToken("alice", TokenSpec{Label: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeToken(info.ID); err != nil {
		t.Fatal(err)
	}
	// Ein zweiter Widerruf schreibt nichts.
	if err := st.RevokeToken(info.ID); err != nil {
		t.Fatal(err)
	}
	id, _ := parsePersonPrincipalID(acct.ID)
	events, err := st.TokenEvents(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	var issued, revoked int
	for _, e := range events {
		if e.TokenID != info.ID {
			continue
		}
		switch e.Action {
		case "issued":
			issued++
		case "revoked":
			revoked++
		}
	}
	if issued != 1 || revoked != 1 {
		t.Fatalf("events for the token: issued %d, revoked %d (%+v)", issued, revoked, events)
	}
	if _, err := st.db.Exec(`UPDATE token_events SET action='issued'`); err == nil {
		t.Fatal("token_events can be changed")
	}
	if _, err := st.db.Exec(`DELETE FROM token_events`); err == nil {
		t.Fatal("token_events can be deleted from")
	}
}

func TestOrgAndMachineEventsAreAppendOnly(t *testing.T) {
	st := openTest(t)
	if _, err := st.db.Exec(`INSERT INTO org_events(org_id, action, created_at) VALUES(1,'x','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO machine_events(hostname, action, created_at) VALUES('h','x','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE org_events SET action='y'`, `DELETE FROM org_events`,
		`UPDATE machine_events SET action='y'`, `DELETE FROM machine_events`,
	} {
		if _, err := st.db.Exec(q); err == nil {
			t.Errorf("%s was allowed", q)
		}
	}
}
