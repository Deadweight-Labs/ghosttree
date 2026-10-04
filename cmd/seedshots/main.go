package main

import (
	"encoding/json"
	"fmt"
	"os"

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
	json.NewEncoder(os.Stdout).Encode(out)
	fmt.Fprintln(os.Stderr, "seeded")
}
