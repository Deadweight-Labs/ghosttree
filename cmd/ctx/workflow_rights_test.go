package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/workflow"
)

// REQ-704 (S03, J6): a member who pushes a workflow learns which role it takes
// and whom to ask; made lead, the same push saves, and a change made in the
// browser meanwhile is shown as a conflict instead of being overwritten. The
// server runs in the log mode of the usability run, where the refusal used to
// come only from the store, with no role and no name.
func TestWorkflowPushAsMemberNamesTheRoleThenTheLeadSavesAndSeesTheBrowsersChange(t *testing.T) {
	e := newWTEnv(t)
	if e.st.AccessEnforced() {
		t.Fatal("the test needs the log mode")
	}
	jonasToken, err := e.st.AddPerson("Jonas Brandt")
	if err != nil {
		t.Fatal(err)
	}
	orgs, err := e.st.ListOrgs("person:1")
	if err != nil {
		t.Fatal(err)
	}
	var orgID int64
	for _, o := range orgs {
		if o.Slug == "alpha" {
			orgID = o.ID
		}
	}
	code, _, err := e.st.CreateInvitation("person:1", orgID, "", store.OrgMember, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetProjectRole("person:1", wtProject, "person:2", store.RoleMember, false, store.RoleViaCLI); err != nil {
		t.Fatal(err)
	}
	base := "---\ndescription: alt\non: pr.merged\nuses: ghosttree/echo\n---\n"
	if res, err := e.c.SaveWorkflow(wtProject, "abgleich", base, "v1", 0); err != nil || res.Saved == nil {
		t.Fatalf("seed: %+v %v", res, err)
	}
	jonas := e
	jonas.c = client.New(config.Config{ServerURL: e.url, Token: jonasToken})
	jonas.root = t.TempDir()
	if code, out := jonas.pull(t, "abgleich", false); code != 0 {
		t.Fatalf("a member pulls: %d %s", code, out)
	}
	mine := strings.Replace(base, "description: alt", "description: von Jonas", 1)
	if err := workflow.WriteWorktreeFile(jonas.root, "abgleich", mine); err != nil {
		t.Fatal(err)
	}
	code1, out := jonas.push(t, "abgleich", "meine Fassung")
	if code1 != 1 || !strings.Contains(out, "403") || !strings.Contains(out, "needs the role lead") || !strings.Contains(out, "your role there is member") || !strings.Contains(out, "Ask robin for the role lead") {
		t.Fatalf("a member's push does not say which role it needs and whom to ask: %d %s", code1, out)
	}
	if d := e.server(t, "abgleich"); d.Workflow.Version != 1 {
		t.Fatalf("a refused push saved: %+v", d.Workflow)
	}

	if err := e.st.SetProjectRole("person:1", wtProject, "person:2", store.RoleLead, false, store.RoleViaWeb); err != nil {
		t.Fatal(err)
	}
	if code, out := jonas.push(t, "abgleich", "meine Fassung"); code != 0 || !strings.Contains(out, "v2") {
		t.Fatalf("the lead's push: %d %s", code, out)
	}
	// Robin changes the workflow in the browser (the same save the web form
	// does), Jonas changes his file on the old version and pushes.
	theirs := strings.Replace(mine, "von Jonas", "von Robin im Browser", 1)
	if _, _, err := workflow.SaveDef(e.st, "person:1", wtProject, "abgleich", theirs, "im Browser", store.WorkflowViaWeb, 2); err != nil {
		t.Fatal(err)
	}
	again := strings.Replace(mine, "von Jonas", "von Jonas, zweite Fassung", 1)
	if err := workflow.WriteWorktreeFile(jonas.root, "abgleich", again); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	code2 := workflowPush(jonas.root, wtProject, jonas.c, "abgleich", "zweite Fassung", &buf)
	out = buf.String()
	if code2 != 1 || !strings.Contains(out, "CONFLICT abgleich") || !strings.Contains(out, "server: version 3") || !strings.Contains(out, "+description: von Robin im Browser") {
		t.Fatalf("the change from the browser is not shown as a conflict: %d\n%s", code2, out)
	}
	if d := e.server(t, "abgleich"); d.Workflow.Version != 3 || d.Workflow.Text != theirs {
		t.Fatalf("the browser's version was overwritten: %+v", d.Workflow)
	}
}
