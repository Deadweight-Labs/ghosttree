package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

func uploadAs(t *testing.T, st *Store, account int64, ext, project string) {
	t.Helper()
	if _, err := st.UpsertSession(Session{Harness: "claude", ExternalID: ext, AccountID: account, Scope: scope.Axes{Project: project, Machine: "m"}}); err != nil {
		t.Fatal(err)
	}
}

func TestListClaimableProjectsIsOwnUnclaimedUploadsForOrgOwnersOnly(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	uploadAs(t, st, 1, "a", "github.com/x/mine")
	uploadAs(t, st, 1, "b", "github.com/x/mine")
	uploadAs(t, st, 1, "c", "github.com/x/claimed")
	uploadAs(t, st, 2, "d", "github.com/x/annas")
	uploadAs(t, st, 1, "e", "")
	if _, err := st.ClaimProject("person:1", "github.com/x/claimed", "alpha"); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListClaimableProjects("person:1", o.ID)
	if err != nil || !reflect.DeepEqual(got, []string{"github.com/x/mine"}) {
		t.Fatalf("owner list %v %v", got, err)
	}
	// Ein Mitglied bekommt keine Liste, auch nicht die eigenen.
	if got, err := st.ListClaimableProjects("person:2", o.ID); !errors.Is(err, ErrNotOrgOwner) || len(got) != 0 {
		t.Fatalf("member list %v %v", got, err)
	}
	// Annas Upload taucht beim Owner nie auf.
	if _, err := st.ClaimProject("person:1", "github.com/x/mine", "alpha"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListClaimableProjects("person:1", o.ID); len(got) != 0 {
		t.Fatalf("claimed project still listed: %v", got)
	}
}

func TestInvitationListShowsWhoAcceptedAndWhen(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, time.Hour)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	invs, err := st.ListInvitations("person:1", o.ID)
	if err != nil || len(invs) != 1 || invs[0].Status != "accepted" || invs[0].AcceptedBy != "anna" || invs[0].AcceptedAt == "" {
		t.Fatalf("%+v %v", invs, err)
	}
}

func TestListClaimableProjectsExcludesRemotesOtherAccountsUploadedTo(t *testing.T) {
	st := orgStore(t, "robin", "anna")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	uploadAs(t, st, 1, "a", "github.com/x/shared")
	uploadAs(t, st, 2, "b", "github.com/x/shared")
	uploadAs(t, st, 1, "c", "github.com/x/mine")
	got, err := st.ListClaimableProjects("person:1", o.ID)
	if err != nil || !reflect.DeepEqual(got, []string{"github.com/x/mine"}) {
		t.Fatalf("list %v %v", got, err)
	}
	if _, _, err := st.ClaimAndInviteProject("person:1", o.ID, "github.com/x/shared", RoleMember, 0); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("shared remote: %v", err)
	}
	if _, ok := st.ProjectByRemote("github.com/x/shared"); ok {
		t.Fatal("shared remote was claimed")
	}
}

func TestClaimAndInviteIsOneTransaction(t *testing.T) {
	st := orgStore(t, "robin")
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	uploadAs(t, st, 1, "a", "github.com/x/mine")
	for i := 0; i < maxPendingInvitations; i++ {
		if _, _, err := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.ClaimAndInviteProject("person:1", o.ID, "github.com/x/mine", RoleMember, 0); !errors.Is(err, ErrTooManyInvites) {
		t.Fatalf("cap: %v", err)
	}
	if _, ok := st.ProjectByRemote("github.com/x/mine"); ok {
		t.Fatal("a refused link left the claim behind")
	}
	invs, _ := st.ListInvitations("person:1", o.ID)
	for _, i := range invs {
		if i.Status == "pending" {
			if err := st.RevokeInvitation("person:1", o.ID, i.ID); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if _, inv, err := st.ClaimAndInviteProject("person:1", o.ID, "github.com/x/mine", RoleMember, 0); err != nil || inv.ProjectRemote != "github.com/x/mine" {
		t.Fatalf("claim and invite: %+v %v", inv, err)
	}
	if p, ok := st.ProjectByRemote("github.com/x/mine"); !ok || p.OrgID != o.ID {
		t.Fatalf("not claimed: %+v", p)
	}
}
