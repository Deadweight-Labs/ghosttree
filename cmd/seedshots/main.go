package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	st, err := store.Open(os.Args[1])
	must(err)
	defer st.Close()
	const project = "github.com/robin-e2e/gemeinsam"
	for i, n := range []string{"Robin", "Lena Berger", "Ben Kraft", "Gus"} {
		_, err := st.AddAccount(n, "", i == 0)
		must(err)
	}
	org, err := st.CreateOrg("person:1", "Robins Werkstatt", "werkstatt")
	must(err)
	for _, who := range []string{"person:2", "person:3", "person:4"} {
		code, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
		must(err)
		_, err = st.AcceptInvitation(who, code)
		must(err)
	}
	_, err = st.EnsureProject("person:1", project)
	must(err)
	must(st.SetProjectRole("person:1", project, "person:2", store.RoleMember, false, store.RoleViaAPI))
	must(st.SetProjectRole("person:1", project, "person:4", store.RoleGuest, false, store.RoleViaAPI))
	st.SetAccessMode(store.AccessMode{Enforce: true})
	room := store.RoomKeyForProject(project)
	reg := func(id, prov, principal string) {
		_, err := st.RegisterCoordAgent(store.CoordAgent{ExternalID: id, Provider: prov, RoomKey: room, PrincipalID: principal, DisplayName: id, Branch: "main"})
		must(err)
	}
	robinAgent := "11111111-aaaa-4bbb-8ccc-000000000001"
	reg(robinAgent, "claude", "person:1")
	reg("claude:lena-laptop:22222222-aaaa-4bbb-8ccc-000000000002", "claude", "person:2")
	reg("claude:gus-box:33333333-aaaa-4bbb-8ccc-000000000003", "claude", "person:4")
	web := store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind}
	first, err := st.CoordinationFor(web, "").Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "h1", Body: "Welcome! README needs an install guide."})
	must(err)
	_, err = st.CoordinationFor(store.Principal{ID: "person:1", Label: "Robin"}, robinAgent).Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, SenderExternalID: robinAgent, ClientID: "a1", Body: "On it.", ReplyTo: first})
	must(err)
	_, err = st.CoordinationFor(store.Principal{ID: "person:4", Label: "Gus", TokenKind: store.WebSessionKind}, "").Send(store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: "g1", Body: "Thanks, looks good to me.", ReplyTo: first, Intent: store.IntentQuestion, Mentions: []string{robinAgent}})
	must(err)
	out := map[string]string{"room": room, "project": project}
	for k, n := range map[string]string{"robin": "Robin", "lena": "Lena Berger", "ben": "Ben Kraft", "gus": "Gus"} {
		c, _, err := st.CreateAccountCode(store.CodeLogin, n)
		must(err)
		out[k] = c
	}
	if len(os.Args) > 2 && os.Args[2] == "pain-tasks" {
		seedPainTasks(st, project, out)
	}
	json.NewEncoder(os.Stdout).Encode(out)
	fmt.Fprintln(os.Stderr, "seeded")
}

// seedPainTasks adds what the pain and task pages show: requests, tasks in each
// state, pains, a device token for Robin (so ctx can talk to the server) and a
// note standing in for the long list of pains that the migration script replaces.
func seedPainTasks(st *store.Store, project string, out map[string]string) {
	robin := st.Access(store.Principal{ID: "person:1", Label: "Robin"})
	lena := st.Access(store.Principal{ID: "person:2", Label: "Lena Berger"})
	robinActor := humantask.Actor{AccountID: 1, Person: "Robin"}
	req := func(title string) int64 {
		d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: project}, Person: "Robin"}, Criteria: []string{"Works end to end"}})
		must(err)
		return d.Request.ID
	}
	billing := req("Connect Stripe for billing")
	schema := req("Migrate the schema to the new store")
	task := func(in humantask.CreateInput) humantask.Task {
		in.Project = project
		t, err := robin.CreateHumanTask(in, robinActor)
		must(err)
		return t
	}
	task(humantask.CreateInput{RequestID: billing, Kind: "secret", Title: "Create the Stripe key", Location: "Agent Vault: stripe/prod", CheckHint: "the entry exists in the vault"})
	task(humantask.CreateInput{RequestID: schema, Kind: "file", Title: "Put the schema dump in place", Location: "./db/schema.sql", CheckHint: "file exists and ends with COMMIT"})
	task(humantask.CreateInput{RequestID: billing, Kind: "input", Title: "Decide on the price tiers", Assignee: "Lena Berger"})
	finished := task(humantask.CreateInput{RequestID: billing, Kind: "action", Title: "Enable the webhook in the Stripe dashboard"})
	_, err := lena.MarkTasksDone([]int64{finished.ID}, "enabled, endpoint answers 200", humantask.Actor{AccountID: 2, Person: "Lena Berger", Web: true})
	must(err)
	actor := pain.Actor{AccountID: 1, Person: "Robin"}
	mk := func(title, body string, to ...pain.Target) int64 {
		d, err := robin.CreatePain(store.CreatePainInput{Project: project, Title: title, Body: body, Motivates: to, Actor: actor})
		must(err)
		return d.Pain.ID
	}
	mk("Reading only works whole", "A read returns the full text, even when only one section is needed.", pain.Target{RequestID: schema})
	half := mk("There is no draft state", "Saving means shipped, searchable and delivered at once.", pain.Target{RequestID: billing}, pain.Target{ExternalRef: "https://example.test/issues/12"})
	_, err = robin.SetPainState(half, "mitigated", "Drafts exist for documents but not for knowledge entries.", pain.Evidence{Kind: "request", Ref: "REQ-" + strconv.FormatInt(schema, 10)}, "", actor)
	must(err)
	done := mk("Reviewers cannot see what changed", "")
	_, err = robin.SetPainState(done, "resolved", "", pain.Evidence{Kind: "commit", Ref: "3b9fecd"}, "", actor)
	must(err)
	note, err := st.InsertKnowledge(store.Knowledge{Type: "note", Title: "PAINS: why a long document is better off in the repo today",
		Body:  "P1 - NO DIFF, NO VERSIONS.\nA spec is written in versions.\n\nP2 - WRITING IS ALL OR NOTHING.\nOne call carries the whole body.\n\nP3 - READING IS ALL OR NOTHING.\nThe whole entry goes into the context.\n",
		Scope: scope.Axes{Project: project}, Person: "Robin", Confidence: "trusted"})
	must(err)
	out["source_note"] = strconv.FormatInt(note, 10)
	token, _, err := st.CreateDeviceToken("person:1", "demo")
	must(err)
	out["robin_token"] = token
}
