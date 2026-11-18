//go:build readmeshots

// Command readme fills a fresh ghosttree database with INVENTED demo data for
// the pictures in the README: the studio "Northwind" with Mia and Theo, four
// projects, agents in every state, requests, tasks, knowledge, an idea in the
// workshop, a workflow run, a runbook and a work package on a pull request.
// Nothing here comes from a real person, project or machine. make.sh in this
// directory builds it, starts a throwaway server and takes the pictures.
//
//	readme <db> team       two people, four projects, 11 agents in every state
//	readme <db> starter    one person, one project, one agent
//	readme <db> empty      nothing (the server then offers its bootstrap code)
//	readme pulse <db> [s]  keep "working" agents working (re-stamps every s seconds)
//
// Prints one JSON object on stdout. Progress goes to stderr.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Deadweight-Labs/ghosttree/internal/chunktext"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "demoseed:", err)
		panic(err)
	}
}

const (
	sf  = "github.com/northwind/storefront"
	ma  = "github.com/northwind/mobile-app"
	pay = "github.com/northwind/payments"
	inf = "github.com/northwind/infra"

	miaID, theoID = "person:1", "person:2"
)

var (
	miaP  = store.Principal{ID: miaID, Label: "Mia"}
	theoP = store.Principal{ID: theoID, Label: "Theo"}
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: demoseed <db> team|starter|empty   |   demoseed pulse <db> [seconds]")
		os.Exit(2)
	}
	if os.Args[1] == "pulse" {
		secs := 30
		if len(os.Args) > 3 {
			secs, _ = strconv.Atoi(os.Args[3])
		}
		pulse(os.Args[2], time.Duration(secs)*time.Second)
		return
	}
	db, dataset := os.Args[1], os.Args[2]
	out := map[string]string{"dataset": dataset}
	switch dataset {
	case "empty":
		// nothing to seed; the server creates the file and the bootstrap code
	case "team":
		seedTeam(db, out)
	case "starter":
		seedStarter(db, out)
	default:
		fmt.Fprintln(os.Stderr, "unknown dataset", dataset)
		os.Exit(2)
	}
	must(json.NewEncoder(os.Stdout).Encode(out))
}

// ---- the agent table (shared by the seed and the pulse) ---------------------

type agentSpec struct {
	key, provider, owner, project, branch, machine, note string
	req                                                  int // index into the request list, -1 = none
	title                                                string
	state                                                string // working, idle, stuck, offline, waiting, waiting_peer
	last                                                 time.Duration
}

func agentUUID(n int) string {
	return fmt.Sprintf("%08x-5e1f-4c0a-9b7d-%012x", 0x1a2b3c00+n, 0x7f00aa000000+n)
}
func (a agentSpec) ext(n int) string {
	return fmt.Sprintf("%s:%s:%s", a.provider, a.machine, agentUUID(n))
}
func home(owner string) string { return map[string]string{miaID: "mia", theoID: "theo"}[owner] }
func (a agentSpec) cwd() string {
	return "/home/" + home(a.owner) + "/Projects/" + a.project[strings.LastIndex(a.project, "/")+1:]
}

var teamAgents = []agentSpec{
	{"a1", "claude", miaID, sf, "feat/guest-checkout", "mia-laptop", "Guest checkout: the payment step without an account", 0, "Guest checkout: payment step", "working", 20 * time.Second},
	{"a2", "claude", miaID, sf, "feat/faster-pages", "mia-laptop", "", 1, "Faster product pages: lazy photos", "idle", 3 * time.Hour},
	{"a3", "claude", miaID, pay, "feat/refunds", "mia-laptop", "Refunds: allow partial amounts", 3, "Refunds from the admin panel", "waiting", 2*time.Hour + 10*time.Minute},
	{"a4", "codex", miaID, ma, "chore/release-notes", "mia-laptop", "", -1, "Release notes for version 2.4", "idle", 5 * time.Hour},
	{"a5", "claude", miaID, pay, "fix/double-receipts", "mia-laptop", "Stop duplicate receipts on payment retries", 4, "Duplicate receipts after a retry", "stuck", 34 * time.Minute},
	{"a6", "claude", miaID, ma, "feat/dark-mode", "mia-laptop", "Dark colours for 14 screens, checking contrast", 5, "Dark mode for the app", "working", 45 * time.Second},
	{"a7", "claude", theoID, sf, "feat/order-emails", "theo-desktop", "Shipping and delivery emails", 2, "Order tracking emails", "working", 15 * time.Second},
	{"a8", "claude", theoID, sf, "feat/order-api", "theo-desktop", "Order status endpoint for the emails", 2, "Order status endpoint", "waiting_peer", 40 * time.Minute},
	{"a9", "claude", theoID, ma, "fix/empty-cart", "theo-desktop", "App closes on an empty cart", 6, "Empty cart crash", "waiting", 70 * time.Minute},
	{"a10", "codex", theoID, inf, "chore/backups", "theo-desktop", "", 7, "Nightly backup job", "idle", 7 * time.Hour},
	{"a11", "claude", miaID, inf, "main", "build-box", "", -1, "Check the last rollout", "offline", 3 * 24 * time.Hour},
}

var starterAgents = []agentSpec{
	{"a1", "claude", miaID, sf, "main", "mia-laptop", "Add a gift-wrap option to the cart", 0, "Gift-wrap option in the cart", "working", 20 * time.Second},
}

// ---- the seeder ---------------------------------------------------------------

type backdate struct {
	table string
	id    int64
	age   time.Duration
}

type seeder struct {
	st        *store.Store
	path      string
	t0        time.Time
	back      []backdate
	extID     map[string]string
	sessID    map[string]int64
	reqID     []int64
	postSQL   []string
	agents    []agentSpec
	cid       int
	mia       *store.ProjectAccess // Mia's access
	theo      *store.ProjectAccess
	titles    map[string]string
	sessAges  map[int64]time.Duration
	sessStart map[int64]time.Duration
}

func newSeeder(path string) *seeder {
	st, err := store.Open(path)
	must(err)
	return &seeder{st: st, path: path, t0: time.Now().UTC(), extID: map[string]string{}, sessID: map[string]int64{},
		titles: map[string]string{}, sessAges: map[int64]time.Duration{}, sessStart: map[int64]time.Duration{}}
}

func (s *seeder) ago(d time.Duration) string { return s.t0.Add(-d).Format(time.RFC3339) }
func (s *seeder) later(table string, id int64, age time.Duration) {
	s.back = append(s.back, backdate{table, id, age})
}

// people: Mia is the owner, Theo (optional) a member of the organisation.
func (s *seeder) people(withTheo bool) {
	names := []string{"Mia"}
	if withTheo {
		names = append(names, "Theo")
	}
	for i, n := range names {
		_, err := s.st.AddAccount(n, "", i == 0)
		must(err)
	}
	// Projects belong to an organisation, so even a one-person instance has one.
	org, err := s.st.CreateOrg(miaID, "Northwind Studio", "northwind")
	must(err)
	if withTheo {
		code, _, err := s.st.CreateInvitation(miaID, org.ID, "", store.OrgMember, 0)
		must(err)
		_, err = s.st.AcceptInvitation(theoID, code)
		must(err)
	}
}

func (s *seeder) projects(withTheo bool, list ...string) {
	for _, p := range list {
		_, err := s.st.ClaimProject(miaID, p, "northwind")
		must(err)
		if withTheo {
			must(s.st.SetProjectRole(miaID, p, theoID, store.RoleMember, false, store.RoleViaAPI))
		}
	}
	s.st.SetAccessMode(store.AccessMode{Enforce: true})
	s.mia = s.st.Access(miaP)
	if withTheo {
		s.theo = s.st.Access(theoP)
	}
}

type reqSpec struct {
	project, typ, title, desc string
	criteria                  []string
	met                       int
	owner                     string
	age                       time.Duration
}

func (s *seeder) requests(specs []reqSpec) {
	prio := []string{"high", "medium", "medium", "medium"}
	for i, r := range specs {
		d, err := s.st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: r.typ, Title: r.title, Description: r.desc,
			Scope: scope.Axes{Project: r.project}, Person: r.owner, Priority: prio[i%4]}, Criteria: r.criteria})
		must(err)
		s.reqID = append(s.reqID, d.Request.ID)
		for j := 0; j < r.met && j < len(d.Criteria); j++ {
			must(s.st.SetCriterionState(d.Criteria[j].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: fmt.Sprintf("b%df3a%dc1", i, j), Person: r.owner}))
		}
		s.later("requests", d.Request.ID, r.age)
	}
}

// registerAgents creates the coordination identity, the session and the work
// link of every agent. Time is expressed as "how long ago the last hook fired".
func (s *seeder) registerAgents(list []agentSpec) {
	s.agents = list
	for i, a := range list {
		ext := a.ext(i + 1)
		s.extID[a.key] = ext
		sess := "s-" + agentUUID(i+1)
		room := store.RoomKeyForProject(a.project)
		reg := store.CoordAgent{ExternalID: ext, Provider: a.provider, RoomKey: room, PrincipalID: a.owner, DisplayName: ext, Branch: a.branch, Machine: a.machine, SessionID: sess, Cwd: a.cwd()}
		acc := int64(1)
		if a.owner == theoID {
			acc = 2
		}
		harness := "claude-code"
		if a.provider == "codex" {
			harness = "codex"
		}
		sid, err := s.st.UpsertSession(store.Session{Harness: harness, ExternalID: sess, Scope: scope.Axes{Project: a.project, Branch: a.branch, Machine: a.machine}, CWD: reg.Cwd, AccountID: acc})
		must(err)
		s.sessID[a.key] = sid
		s.titles[sess] = a.title
		s.sessAges[sid] = a.last
		s.sessStart[sid] = a.last + 95*time.Minute
		first := reg
		first.RegisteredAt = s.ago(a.last + 90*time.Minute)
		first.HookActivity, first.HookEvent = true, store.HookTurnEnd
		_, err = s.st.RegisterCoordAgent(first)
		must(err)
		last := reg
		last.RegisteredAt = s.ago(a.last)
		last.HookActivity = true
		switch a.state {
		case "working", "stuck":
			last.HookEvent = store.HookTool
		default:
			last.HookEvent = store.HookTurnEnd
		}
		_, err = s.st.RegisterCoordAgent(last)
		must(err)
		if a.state != "offline" && a.state != "idle" {
			must(s.st.TouchCoordAgentPoll(ext))
		}
		if a.note != "" {
			_, err = s.st.SetCoordWorkNote(ext, a.owner, a.note)
			must(err)
		}
		if a.req >= 0 {
			person := map[string]string{miaID: "Mia", theoID: "Theo"}[a.owner]
			_, _, err = s.st.StartRequestWork(s.reqID[a.req], sid, "primary", person)
			must(err)
		}
	}
}

// say posts a room message. from is "mia", "theo" or an agent key.
func (s *seeder) say(from, project, body, intent string, mention []string, replyTo int64, age time.Duration, expl string) int64 {
	s.cid++
	room := store.RoomKeyForProject(project)
	m := store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: fmt.Sprintf("seed-%d", s.cid), Body: body, Intent: intent, Mentions: mention, ReplyTo: replyTo, Explanation: expl}
	var id int64
	var err error
	switch from {
	case "mia":
		id, err = s.st.CoordinationFor(store.Principal{ID: miaID, Label: "Mia", TokenKind: store.WebSessionKind}, "").Send(m)
	case "theo":
		id, err = s.st.CoordinationFor(store.Principal{ID: theoID, Label: "Theo", TokenKind: store.WebSessionKind}, "").Send(m)
	default:
		m.SenderExternalID = s.extID[from]
		owner := miaID
		for _, ag := range s.agents {
			if ag.key == from {
				owner = ag.owner
			}
		}
		p := miaP
		if owner == theoID {
			p = theoP
		}
		id, err = s.st.CoordinationFor(p, s.extID[from]).Send(m)
	}
	must(err)
	if expl != "" {
		must(s.st.SaveAgentExplanation("question", id, expl))
	}
	s.later("coord_messages", id, age)
	return id
}

func (s *seeder) task(acc *store.ProjectAccess, actor humantask.Actor, project string, in humantask.CreateInput, expl string, age time.Duration) humantask.Task {
	in.Project = project
	t, err := acc.CreateHumanTask(in, actor)
	must(err)
	if expl != "" {
		must(s.st.SaveAgentExplanation("task", t.ID, expl))
	}
	s.later("human_tasks", t.ID, age)
	return t
}

func (s *seeder) pain(project, title, body string, age time.Duration, motivates ...pain.Target) int64 {
	d, err := s.mia.CreatePain(store.CreatePainInput{Project: project, Title: title, Body: body, Motivates: motivates, Actor: pain.Actor{AccountID: 1, Person: "Mia"}})
	must(err)
	s.later("pains", d.Pain.ID, age)
	return d.Pain.ID
}

type kn struct {
	project, typ, title, body, conf, origin, harness, person string
	age                                                      time.Duration
	expl                                                     string
}

func (s *seeder) knowledge(items []kn) []int64 {
	var ids []int64
	for _, k := range items {
		sessionRef := ""
		if k.harness == "gardener" {
			sessionRef = "gardener:check-knowledge:1"
		}
		id, err := s.st.InsertKnowledge(store.Knowledge{Type: k.typ, Title: k.title, Body: k.body, Scope: scope.Axes{Project: k.project}, Person: k.person,
			Confidence: k.conf, Origin: k.origin, Harness: k.harness, SessionRef: sessionRef, ConfirmedBy: map[bool]string{true: k.person, false: ""}[k.conf == "verified"]})
		must(err)
		ids = append(ids, id)
		if k.expl != "" {
			must(s.st.SaveAgentExplanation("knowledge", id, k.expl))
		}
		s.later("knowledge", id, k.age)
	}
	return ids
}

// ---- transcripts ----------------------------------------------------------------

type transcript struct {
	s          *seeder
	sid        int64
	ext, cwd   string
	branch     string
	harness    string
	t          time.Time
	seq        int
	chunks     []store.Chunk
	nextToolID int
}

func (s *seeder) transcript(key string, startedAgo time.Duration) *transcript {
	var a agentSpec
	for _, x := range s.agents {
		if x.key == key {
			a = x
		}
	}
	return &transcript{s: s, sid: s.sessID[key], ext: "s-" + agentUUID(indexOf(s.agents, key)+1), cwd: a.cwd(), branch: a.branch, harness: "claude-code",
		t: s.t0.Add(-startedAgo)}
}

func indexOf(list []agentSpec, key string) int {
	for i, a := range list {
		if a.key == key {
			return i
		}
	}
	return 0
}

func (t *transcript) line(typ string, msg map[string]any, extra map[string]any) {
	t.t = t.t.Add(7 * time.Second)
	m := map[string]any{"sessionId": t.ext, "cwd": t.cwd, "gitBranch": t.branch, "isSidechain": false, "type": typ,
		"timestamp": t.t.Format("2006-01-02T15:04:05.000Z"), "message": msg}
	for k, v := range extra {
		m[k] = v
	}
	raw, err := json.Marshal(m)
	must(err)
	p := chunktext.ParseClaudeLine(raw)
	t.seq++
	t.chunks = append(t.chunks, store.Chunk{Seq: t.seq, Role: p.Role, Text: p.Text, Raw: string(raw)})
}

func (t *transcript) user(text string) {
	t.line("user", map[string]any{"role": "user", "content": text}, nil)
}
func (t *transcript) think(text string) {
	t.line("assistant", map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": text, "signature": "x"}}}, nil)
}

type call struct {
	name   string
	input  map[string]any
	result string
}

// say adds an assistant turn: optional text, then tool calls with their results.
func (t *transcript) say(text string, calls ...call) {
	var content []any
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	ids := make([]string, len(calls))
	for i, c := range calls {
		t.nextToolID++
		ids[i] = fmt.Sprintf("toolu_%03d", t.nextToolID)
		content = append(content, map[string]any{"type": "tool_use", "id": ids[i], "name": c.name, "input": c.input})
	}
	t.line("assistant", map[string]any{"role": "assistant", "content": content}, nil)
	for i, c := range calls {
		t.line("user", map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": ids[i], "content": c.result}}}, map[string]any{"toolUseResult": map[string]any{"stdout": "x"}})
	}
}

func (t *transcript) flush() {
	must(t.s.st.AppendChunks(t.sid, t.chunks))
	t.s.later("session_last", t.sid, 0)
}

func bash(cmd, out string) call {
	return call{"Bash", map[string]any{"command": cmd}, out}
}
func read(path, out string) call { return call{"Read", map[string]any{"file_path": path}, out} }
func edit(path, oldS, newS string) call {
	return call{"Edit", map[string]any{"file_path": path, "old_string": oldS, "new_string": newS}, "The file " + path + " has been updated."}
}

// ---- dataset: team ----------------------------------------------------------------

func seedTeam(path string, out map[string]string) {
	s := newSeeder(path)
	s.people(true)
	s.projects(true, sf, ma, pay, inf)
	mia := humantask.Actor{AccountID: 1, Person: "Mia"}
	theo := humantask.Actor{AccountID: 2, Person: "Theo"}

	d := 24 * time.Hour
	s.requests([]reqSpec{
		{sf, "feature", "Guest checkout", "Shoppers should be able to pay without creating an account. Right now four in ten carts are abandoned at the sign-up step.",
			[]string{"Shoppers can pay without an account", "A confirmation email arrives within a minute", "The page works on phones and tablets", "Tested with all three payment methods"}, 2, "Mia", 6 * d},
		{sf, "change", "Faster product pages", "Product pages take about four seconds to show their photos on a phone. The goal is under two.",
			[]string{"Photos load as you scroll", "Page weight stays under 1 MB", "Speed score above 90 on a mid-range phone"}, 1, "Mia", 9 * d},
		{sf, "feature", "Order tracking emails", "Customers want to know where their parcel is. Send an email when an order ships and another when it arrives.",
			[]string{"Email when an order ships", "Email when it is delivered", "Customers can switch the emails off"}, 1, "Theo", 4 * d},
		{pay, "feature", "Refunds from the admin panel", "Support staff should be able to refund an order without asking a developer.",
			[]string{"Full and partial refunds", "The customer gets a receipt", "Every refund is logged", "A refund limit for new staff"}, 2, "Mia", 12 * d},
		{pay, "bug", "Customers get two receipts after a payment retry", "When the payment provider is slow and the shop retries, the customer receives the receipt twice.",
			[]string{"Cause found", "A retry never sends a second receipt", "Fix tested with a simulated outage"}, 1, "Theo", 2 * d},
		{ma, "feature", "Dark mode", "The app should follow the phone's light or dark setting.",
			[]string{"Colours follow the phone setting", "Text contrast is checked", "The choice is remembered"}, 2, "Mia", 5 * d},
		{ma, "bug", "The app closes when the cart is empty", "Opening the cart with nothing in it closes the app. Reported by 31 people last week.",
			[]string{"Cause found", "An empty cart shows a friendly message", "Added to the release checklist"}, 1, "Theo", 3 * d},
		{inf, "change", "Nightly backups to cold storage", "Back up every database each night to cheaper storage and keep two weeks of history.",
			[]string{"Backups run every night", "Copies older than 14 days are removed", "A restore was tested once"}, 0, "Mia", 8 * d},
	})
	s.registerAgents(teamAgents)

	// ---- room messages
	r1 := []string{miaID}
	s.say("a7", sf, "Starting with the shipping email. Theo's status endpoint will return the status, the carrier and the expected date, so I will build against that.", "", nil, 0, 3*time.Hour+20*time.Minute, "")
	q := s.say("a8", sf, "Customers get confused when a tracking link leaves our site. Should the status endpoint show the carrier's own link, or ours?", store.IntentQuestion, []string{s.extID["a7"]}, 0, 3*time.Hour, "")
	s.say("a7", sf, "Ours. We keep the customer on our page and show the carrier name underneath.", "", nil, q, 2*time.Hour+50*time.Minute, "")
	s.say("a1", sf, "Guest checkout now works end to end on my branch. After paying, should we offer shoppers a one-tap account?", store.IntentQuestion, r1, 0, 95*time.Minute,
		"Guest shoppers can pay without signing up. The agent asks whether to offer a one-tap account after the purchase. It is a product decision, not a technical one. Recommendation: offer it, it is optional and often turns buyers into members.")
	s.say("a7", sf, "The delivery email needs a short product list. May I show the first three items and write \"and 2 more\" for bigger orders?", store.IntentApproval, r1, 0, 25*time.Minute,
		"The email would list at most three products and summarise the rest. You approve the wording before it is built.")
	s.say("mia", sf, "Please keep every customer email under 120 words. Nobody reads more.", "", []string{s.extID["a7"]}, 0, 4*time.Hour, "")

	s.say("a3", pay, "Refunds above 200 dollars could need a second person's sign-off. Until I hear back I will build the simple version. Should large refunds need a second approval?", store.IntentQuestion, r1, 0, 2*time.Hour+10*time.Minute,
		"Large refunds could require a second pair of eyes. That adds a step for staff but protects you from mistakes. Recommendation: start simple and add the second approval later if it is ever needed.")
	s.say("a5", pay, "Found it: when the provider answers slowly we ask again, and each answer sends a receipt. I will send the receipt once, when the order is marked paid.", "", nil, 0, 2*time.Hour+30*time.Minute, "")
	s.say("theo", pay, "Heads up: the fix must also cover refunds, the same receipt logic is used there.", "", []string{s.extID["a5"]}, 0, 50*time.Minute, "")

	s.say("mia", ma, "The dark mode screens need to look as calm as the light ones. No pure black backgrounds, please.", "", []string{s.extID["a6"]}, 0, 6*time.Hour, "")
	s.say("a6", ma, "Understood. I am using a deep blue-grey instead of black. Fourteen screens are done, six to go.", "", nil, 0, 5*time.Hour+40*time.Minute, "")
	s.say("a6", ma, "Dark mode is done for 14 screens. The grey text on the order screen is too faint (contrast 3.8, we need 4.5). May I make that grey text lighter?", store.IntentApproval, r1, 0, 40*time.Minute,
		"One text colour is hard to read on the dark background. The agent wants to make it a bit lighter. It changes the look slightly but stays inside the colour palette.")
	s.say("a9", ma, "The app closes on an empty cart because no message exists for it. What should an empty cart say?", store.IntentQuestion, []string{theoID}, 0, 70*time.Minute,
		"The app currently closes on an empty cart. The agent needs a decision on the wording of the replacement screen. Recommendation: one short friendly sentence plus a button back to the shop.")
	s.say("theo", ma, "Short sentence plus button, and the same wording on the web shop please.", "", []string{s.extID["a9"]}, 0, 50*time.Minute, "")

	s.say("a10", inf, "The backup job is written and tested on a small copy. It still needs access to the storage space before the first real run.", store.IntentBlocker, []string{theoID}, 0, 7*time.Hour, "")
	s.say("a2", sf, "Faster product pages are merged: the speed score went from 61 to 93. One small item is left, see the request.", store.IntentHandoff, r1, 0, 3*time.Hour,
		"The work on faster product pages is finished and handed over. One small item remains. Accept the handover if you want to finish it yourself.")
	s.say("a11", inf, "Hand-over: last rollout went fine, the disk on the build box is 82 percent full and should be looked at.", store.IntentHandoff, []string{theoID}, 0, 3*d, "")

	// ---- tasks for humans
	s.task(s.mia, mia, pay, humantask.CreateInput{RequestID: s.reqID[3], Kind: "secret", Title: "Add the live payment key to the vault",
		Description: "For real money the shop needs its own restricted key from the payment provider (checkout and refunds only, no payouts). Create it in the provider's dashboard under Developers > API keys, copy it and store it in the vault. Please never paste the key into the chat or a file.",
		Location:    "Agent Vault: payments/provider/live (field secret_key)",
		CheckHint:   "The entry exists; the first characters of the key start with the provider's live prefix."},
		"The agent can only switch to real payments when the key is in the vault. You create it at the provider and store it there; the agent only checks that the entry exists. Nothing of it ever appears in the chat.", 26*time.Hour)
	s.task(s.mia, mia, sf, humantask.CreateInput{RequestID: s.reqID[1], Kind: "file", Title: "Put the new product photos in place",
		Description: "The photographer's final export (about 340 images) has to go into the shop's assets folder so the faster page loading can be tested with real pictures.",
		Location:    "assets/products/2026-autumn/",
		CheckHint:   "The folder exists and holds more than 300 .jpg files."},
		"The speed work needs the real photos to measure properly. They only exist on your machine, so the agent asks you to place them.", 3*d)
	s.task(s.mia, mia, sf, humantask.CreateInput{RequestID: s.reqID[0], Kind: "input", Title: "Choose the shipping price tiers", Assignee: "Theo",
		Description: "The checkout needs final shipping prices. Options: A) flat 4.90 for everything, B) free above 50, otherwise 5.90, C) 3.90 for small parcels and 7.90 for large ones. Please answer with a letter and one sentence of reasoning."},
		"The checkout cannot be finished without fixed prices. Three options are on the table; Theo picks one. It is a business decision, not for the agent to make.", 20*time.Hour)
	s.task(s.mia, mia, inf, humantask.CreateInput{RequestID: s.reqID[7], Kind: "action", Title: "Point the staging address to the new server",
		Description: "Staging needs an address record. In the DNS settings add an A record named staging that points to 203.0.113.24, with the proxy switched off.",
		Location:    "DNS settings of the studio's domain",
		CheckHint:   "Looking up staging.northwind.example returns 203.0.113.24.",
		Steps: []humantask.Step{{Text: "Open the DNS settings of northwind.example"}, {Text: "Add an A record: name staging, value 203.0.113.24"},
			{Text: "Switch the proxy off for this record"}, {Text: "Save and wait about five minutes"}, {Text: "Mark this task done; the agent checks the address"}}},
		"The test site has no address yet. This is a one-time step in the DNS settings that only you can do.", 7*time.Hour)
	s.task(s.mia, mia, sf, humantask.CreateInput{RequestID: s.reqID[0], Kind: "action", Everyone: true, Title: "Renew the shop's domain name",
		Description: "The domain expires in nine days. Renew it for two years with the registrar. Whoever of you has a minute first."},
		"If the domain expires, the shop goes offline. It does not matter who renews it, but one of you must.", 2*d+3*time.Hour)
	s.task(s.theo, theo, pay, humantask.CreateInput{RequestID: s.reqID[3], Kind: "input", Title: "Ask the accountant about the receipt wording",
		Description: "Receipts must name the company and VAT number. Please check with the accountant that the new wording is acceptable and note the answer here."},
		"Receipts are legal documents. Only a person can confirm the wording with the accountant, so the agent waits for the answer.", 5*time.Hour)
	done := s.task(s.mia, mia, pay, humantask.CreateInput{RequestID: s.reqID[3], Kind: "action", Title: "Switch on refund notifications at the provider", Assignee: "Theo",
		Description: "Switch on the notifications for a refund that was created and a refund that failed, for the shop's endpoint."}, "", 30*time.Hour)
	_, err := s.theo.MarkTasksDone([]int64{done.ID}, "switched on, test event arrived", humantask.Actor{AccountID: 2, Person: "Theo", Web: true})
	must(err)

	// ---- pains
	s.pain(sf, "Suggestions pile up and nobody feels responsible",
		"Twenty-six suggestions are waiting for review, four of them for two weeks. When you open the page you cannot tell which ones matter. If nobody reviews them, the assistants keep working with outdated knowledge.", 6*d)
	s.pain(sf, "Assistants lose the thread in long sessions",
		"After a long conversation is shortened, the assistant still knows the task but forgets which file it wanted to change and why. It starts searching again. Seen in seven of the last ten long sessions.", 11*d)
	m := s.pain(pay, "Database changes go live without a dry run",
		"The last change locked the orders table for twelve minutes in the middle of the day. A dry run against a copy would have shown it.", 17*d)
	_, err = s.mia.SetPainState(m, "mitigated", "A dry run exists for the shop, not yet for payments.", pain.Evidence{Kind: "commit", Ref: "5d2a7c9"}, "", pain.Actor{AccountID: 1, Person: "Mia"})
	must(err)
	s.pain(ma, "Nobody sees how far a release is", "The release checklist lives in three places. Before every release somebody asks in the chat which items are done.", 4*d)
	r := s.pain(ma, "Dark colours drift apart between design and code", "The design tool and the app use slightly different greys. The difference only shows up on a real phone.", 25*d)
	_, err = s.mia.SetPainState(r, "resolved", "", pain.Evidence{Kind: "commit", Ref: "9c1e4f2"}, "", pain.Actor{AccountID: 1, Person: "Mia"})
	must(err)

	// ---- knowledge: what is already known (verified) first, so the gardener can point at real ids
	verified := func(project, typ, title, body string, age time.Duration, expl ...string) kn {
		e := ""
		if len(expl) > 0 {
			e = expl[0]
		}
		return kn{project, typ, title, body, "verified", "human", "", "Mia", age, e}
	}
	known := s.knowledge([]kn{
		verified(sf, "instruction", "Open a pull request, never push straight to main", "All changes go through a pull request, even small ones. The main branch is protected.", 40*d, "A house rule for every change: a second person looks at it before it goes live. Assistants read this before they touch the code."),
		verified(sf, "instruction", "Customer-facing text is plain and friendly", "No jargon in anything a customer reads. Short sentences, active voice, no exclamation marks.", 38*d, "A writing rule for everything a customer reads. Assistants follow it when they write emails and screen texts."),
		verified(pay, "instruction", "Secrets live in the vault, never in the chat", "Never paste keys or passwords into a conversation or a file. Store them in the vault and mention only where they live.", 35*d, "A safety rule: passwords and keys never appear in a conversation. Assistants are told where they are kept instead."),
		verified(sf, "note", "Deploys start when a version tag is pushed", "A tag starting with v starts the release pipeline. The live shop is updated by hand afterwards.", 30*d, "How a release really starts: with a tag, not with a button. Saves an assistant from looking for a deploy command that does not exist."),
		verified(sf, "note", "The shop's server restarts every night at 04:00", "A job that is running at that time will be cut off. Long tasks should finish before 03:45.", 28*d, "A timing fact about the shop. Long jobs should be done by 03:45 or they get cut off."),
		verified(sf, "pitfall", "Prices are stored in cents", "Never calculate with decimal dollars. 4.90 is stored as 490, otherwise totals are off by a cent now and then.", 33*d, "A classic trap with money: prices are whole cents, so totals stay exact."),
		verified(pay, "decision", "Receipts are sent once, when the order is marked paid", "Decided after the double-receipt bug. Retries of the payment never trigger another receipt.", 20*d, "A decision with its reason. After customers got two receipts, the team agreed that only the first successful payment sends one."),
		verified(pay, "pitfall", "The provider's test mode accepts cards that real mode rejects", "A passing test payment does not prove a card type works live. Try one real small payment before launch.", 31*d, "A warning from experience: a green test does not prove that a payment method works for real customers."),
		verified(ma, "note", "Release candidates go to the testers' phones on Thursdays", "Anything not merged by Wednesday evening waits for the next candidate.", 26*d, "The weekly rhythm of the app: merge by Wednesday evening, testers get it on Thursday."),
		verified(inf, "decision", "Backups: 14 nightly copies, then 8 weekly", "More history only costs storage and nobody has ever restored anything older than ten days.", 7*d, "A decision about how long backups are kept, and why more history is not worth the cost."),
		verified(inf, "note", "The staging site is reset every night at 03:00", "Anything created there by hand is gone the next morning. Test data belongs in the seed script.", 22*d, "A reminder that nothing made by hand on the staging site survives the night."),
	})
	// ---- suggestions waiting for review
	prop := func(project, typ, title, body, person string, age time.Duration, expl string) kn {
		return kn{project, typ, title, body, "staged", "agent", "claude", person, age, expl}
	}
	dist := func(project, typ, title, body string, age time.Duration) kn {
		return kn{project, typ, title, body, "quarantined", "distilled", "claude", "Mia", age, ""}
	}
	garden := func(project string, id int64, label, subject, reason string, age time.Duration) kn {
		return kn{project, "note", fmt.Sprintf("[Gardener] %s #%d: %s", label, id, subject),
			fmt.Sprintf("Suggestion by the gardener (origin gardener, not confirmed) about entry #%d: %s\n\nVerdict: %s\n\nReason: %s\n", id, subject, label, reason),
			"staged", "distilled", "gardener", "gardener", age,
			"The gardener re-read this entry and thinks it no longer holds: " + reason + " You confirm that it is outdated, or reject the suggestion. The original entry stays unchanged until you decide."}
	}
	s.knowledge([]kn{
		prop(sf, "pitfall", "A shopper's address with an ampersand breaks the confirmation link", "Names like \"Smith & Sons\" are cut off in the link of the confirmation email. Encode the whole address once, never piece by piece.", "Mia", 14*d,
			"A trap in the email templates: some company names break the link. Once confirmed, future assistants read the warning before they touch emails."),
		prop(sf, "decision", "Guest checkout asks for an email, not a password", "Decided on 14 October: a guest only gives an email and an address. Rejected: asking for a phone number, because it lowers the share of completed orders.", "Mia", 6*time.Hour,
			"A product decision with its reason, so nobody turns it around by accident later."),
		prop(sf, "pitfall", "Shipping emails are not sent in test mode", "When the shop runs in test mode, the mail service silently drops every message. Check the sending log before debugging the template.", "Theo", 14*d, ""),
		prop(sf, "note", "The product page loads its fonts about a third of a second late", "Screenshots taken straight after opening the page show the fallback font. Wait for the fonts before taking one.", "Mia", 9*d, ""),
		prop(sf, "pitfall", "A second program that writes to the database blocks the shop", "Two writers on the same database file block each other until a timeout. Stop the shop before running a script that edits data.", "Theo", 3*d,
			"Warns against editing the database from the outside while the shop is running."),
		prop(sf, "decision", "Every customer email is written in both languages", "The catalogue test demands that each new text exists in English and German. A missing translation fails the build.", "Mia", 2*d, ""),
		prop(pay, "pitfall", "The provider's signature check needs the untouched message", "If the message is parsed and written back before the check, the signature never matches. Read the raw bytes first, check, then parse.", "Mia", 4*d,
			"A classic trap with payment providers that has already cost time once."),
		prop(pay, "decision", "Refund list scrolls by cursor, not by page number", "The admin list continues from the last row seen. Page numbers jump when new refunds arrive and get slow on big tables.", "Theo", 3*time.Hour, "Records why the list works the way it does."),
		prop(pay, "note", "The test account is wiped every Sunday night", "Everything created in the provider's test account by hand is gone on Monday. Test data belongs in the seed script.", "Theo", 11*d, ""),
		prop(pay, "pitfall", "Excel shows umlauts wrongly without a byte-order mark", "Exports for the accountant need a UTF-8 marker at the start, otherwise accents turn into odd symbols. Use semicolons as separator.", "Theo", 90*time.Minute, ""),
		prop(pay, "pitfall", "The orders table locks for twelve minutes when a column is added carelessly", "Add the column as optional first, fill it, then make it required. One step at once locks the table.", "Mia", 5*d,
			"A mistake from real life that would repeat itself on the next database change."),
		prop(ma, "pitfall", "Opening the cart without items closes the app", "The cart screen assumes at least one item. Check for an empty list before reading the first element.", "Mia", 2*time.Hour, ""),
		prop(ma, "note", "Times in the order history are mixed between local time and UTC", "Some orders carry a time zone, some do not. The assumption for the second group has to be decided (open question to Theo).", "Theo", 70*time.Minute, ""),
		prop(ma, "decision", "The app does not use pure black in dark mode", "Backgrounds are a deep blue-grey so that photos and text stay calm. Pure black looked harsh in the tests.", "Mia", 8*d, ""),
		prop(ma, "pitfall", "Grey text on the order screen fails the contrast test in dark mode", "Contrast is 3.8:1 instead of 4.5:1. Lighten the grey a little.", "Mia", 40*time.Minute, ""),
		prop(inf, "pitfall", "Never keep the backup password next to the backups", "A password file next to the repository makes the encryption pointless.", "Theo", 6*time.Hour, "A security trap with the new backup job."),
		prop(inf, "decision", "Backups are kept for 14 days, then pruned", "Daily for 14 days, weekly for 8 weeks. More history only costs storage.", "Theo", 7*time.Hour, ""),
		dist(sf, "note", "Reviewing a suggestion means pressing confirm or reject", "Derived from a session, not confirmed yet.", 4*d),
		dist(pay, "note", "Orders have four states: new, paid, shipped, refunded", "Derived from a session, not confirmed yet.", 5*d),
		dist(ma, "pitfall", "The 2024 order export uses semicolons as separator", "Derived from a session. Newer files use commas.", 6*d),
		garden(sf, known[3], "Outdated?", "Deploys start when a version tag is pushed", "Since the pipeline change the live shop updates automatically after the tag; the manual step is gone.", 20*time.Hour),
		garden(sf, known[4], "Correction?", "The shop's server restarts every night at 04:00", "The restart moved to 03:30 when the server was resized last month.", 20*time.Hour),
		garden(pay, known[7], "Outdated?", "The provider's test mode accepts cards that real mode rejects", "The provider closed this gap in its September update; test mode now behaves like live mode.", 20*time.Hour),
		garden(inf, known[10], "Outdated?", "The staging site is reset every night at 03:00", "Staging is no longer reset. It keeps its data since the new server.", 20*time.Hour),
	})
	// The gardener also checked entries that are fine ("checked N entries" in the report).
	knownProject := []string{sf, sf, pay, sf, sf, sf, pay, pay, ma, inf, inf}
	for i, id := range known {
		if i == 3 || i == 4 || i == 7 || i == 10 { // the four the gardener flagged above
			continue
		}
		must(s.st.SaveGardenCheck(knownProject[i], store.GardenCheck{KnowledgeID: id, CheckedAt: s.ago(5*time.Hour + time.Duration(i)*time.Minute), Verdict: "holds"}))
	}

	// ---- three transcripts with real content, short ones for the rest
	t := s.transcript("a1", 95*time.Minute)
	t.user("Let shoppers check out without creating an account. Keep it as simple as possible: email, address, pay.")
	t.think("The checkout currently redirects to the sign-in page when there is no session. I should look at how the cart is turned into an order and where the account is required.")
	t.say("I'll start by finding where the checkout requires a signed-in user.",
		bash("grep -rn \"requireAccount\" src/checkout", "src/checkout/routes.ts:18:  router.use(requireAccount)\nsrc/checkout/routes.ts:44:  router.post('/pay', requireAccount, payHandler)"))
	t.say("Both the page and the payment call insist on an account. I'll read the payment handler first.",
		read("src/checkout/payHandler.ts", "export async function payHandler(req, res) {\n  const user = req.user  // always set by requireAccount\n  const order = await createOrder(user.id, req.cart)\n  ..."))
	t.think("createOrder needs a user id. For guests I can store the email on the order and leave the user empty. The confirmation email must go to that address.")
	t.say("Orders can belong to a guest if I store the email on the order itself. I'll make the account optional.",
		edit("src/checkout/routes.ts", "router.use(requireAccount)", "router.use(optionalAccount)"),
		edit("src/checkout/payHandler.ts", "const order = await createOrder(user.id, req.cart)", "const order = await createOrder({ userId: req.user?.id ?? null, email: req.body.email }, req.cart)"))
	t.say("Now a test for the new path: a visitor pays without signing in and receives a confirmation.",
		bash("npm test -- guest-checkout", " PASS  src/checkout/guest-checkout.test.ts\n  guest checkout\n    ✓ creates an order for a visitor without an account (84 ms)\n    ✓ sends the confirmation to the email entered (61 ms)\n    ✓ still links the order to the account when someone is signed in (45 ms)\n\nTests: 3 passed, 3 total"))
	t.say("Guest checkout works end to end on this branch: a visitor enters an email and an address, pays, and gets the confirmation email. Signed-in customers are unaffected. One open decision for you: after paying, should we offer to create an account with one tap? I asked in the project room.")
	t.user("Good. Offer it, but only as a small link, not a big banner.")
	t.say("Understood, a small link under the confirmation. I'll add it now.",
		edit("src/checkout/Confirmation.tsx", "<ThankYou order={order} />", "<ThankYou order={order} />\n      {order.guest && <a className=\"quiet-link\" href=\"/account/create\">Save these details for next time</a>}"))
	t.flush()

	t = s.transcript("a5", 150*time.Minute)
	t.user("Customers sometimes get two receipts after paying. Find out why and fix it.")
	t.think("Receipts are probably sent from the payment callback. If the provider retries the callback, the handler runs twice. Let me find where the receipt is sent.")
	t.say("Let me look for where receipts are sent.",
		bash("grep -rn \"sendReceipt\" src", "src/payments/callback.ts:57:    await sendReceipt(order)\nsrc/payments/retry.ts:23:    await sendReceipt(order)"))
	t.say("There are two places that send a receipt: the callback and the retry job. Let me read both.",
		read("src/payments/retry.ts", "export async function retryPending() {\n  for (const order of await pendingOrders()) {\n    const res = await provider.status(order.paymentId)\n    if (res.paid) { await markPaid(order); await sendReceipt(order) }\n  }\n}"))
	t.think("Both paths mark the order paid and send a receipt. If the callback arrives while the retry job is already looking at the same order, both send. The receipt should depend on the order moving from pending to paid exactly once.")
	t.say("The cause: the retry job and the provider's callback can both see the same order as unpaid and both send a receipt. I'll make \"mark as paid\" report whether it actually changed anything, and only send the receipt then.",
		edit("src/payments/orders.ts", "export async function markPaid(order) {\n  await db.update(order.id, { status: 'paid' })\n}", "export async function markPaid(order): Promise<boolean> {\n  const changed = await db.updateWhere({ id: order.id, status: 'pending' }, { status: 'paid' })\n  return changed === 1\n}"))
	t.say("Testing with a simulated slow provider.",
		bash("npm test -- receipts", " FAIL  src/payments/receipts.test.ts\n  receipts\n    ✓ sends one receipt for a normal payment (52 ms)\n    ✕ sends one receipt when callback and retry run at once (1210 ms)\n\nExpected 1 receipt, received 2"))
	t.flush()

	t = s.transcript("a6", 120*time.Minute)
	t.user("Add dark mode to the app. Follow the phone setting, and remember the choice if the person overrides it.")
	t.think("The colours are defined in one theme file. I should add a dark palette there and switch by the system appearance, then store an override in settings.")
	t.say("I'll start with the theme file to see how colours are defined.",
		read("Sources/Theme/Colors.swift", "enum Colors {\n    static let background = Color(hex: 0xFFFFFF)\n    static let text = Color(hex: 0x1A2333)\n    static let secondaryText = Color(hex: 0x8A97A8)\n    ..."))
	t.say("Every colour is a fixed value. I'll turn them into pairs of light and dark colours, starting with the background and the two text colours.",
		edit("Sources/Theme/Colors.swift", "static let background = Color(hex: 0xFFFFFF)", "static let background = Color(light: 0xFFFFFF, dark: 0x141A24)"),
		edit("Sources/Theme/Colors.swift", "static let secondaryText = Color(hex: 0x8A97A8)", "static let secondaryText = Color(light: 0x8A97A8, dark: 0xA3AFBF)"))
	t.say("Checking contrast for every text colour on the dark background.",
		bash("swift run contrast-check --theme dark", "text            on background   13.9  AA ok\nsecondaryText   on background    6.4  AA ok\nmutedText       on surface-2     3.8  AA FAIL (needs 4.5)"))
	t.say("One text colour is too faint on the dark background. I asked in the project room whether I may lighten it a little.")
	t.flush()

	for _, mini := range []struct{ key, prompt, reply string }{
		{"a2", "Make the product pages load faster on phones.", "Photos now load as you scroll and the speed score went from 61 to 93. Merged."},
		{"a3", "Support staff need to refund orders from the admin panel.", "Full refunds work. For partial refunds I need a decision on large amounts, I asked in the project room."},
		{"a4", "Write the release notes for version 2.4 in plain language.", "Done: six short bullet points, no technical terms. Saved as RELEASE_NOTES.md."},
		{"a7", "Send a customer an email when their order ships.", "The shipping email is built. For delivery emails I am waiting for the status endpoint."},
		{"a8", "Build an endpoint that tells whether an order is on its way or delivered.", "The endpoint returns the status and the carrier. I asked which tracking link the customer should see."},
		{"a9", "The app closes when the cart is empty. Fix it.", "Found it: the cart screen reads the first item without checking. I need to know which message the empty cart should show."},
		{"a10", "Write a job that backs up all databases every night.", "The job works on a small copy. It still needs access to the storage space."},
		{"a11", "Check the last rollout on the build box.", "The rollout was fine. The disk is 82 percent full."},
	} {
		t := s.transcript(mini.key, s.sessStart[s.sessID[mini.key]])
		t.user(mini.prompt)
		t.say(mini.reply)
		t.flush()
	}

	s.extras()

	// ---- logins
	tokM, _, err := s.st.CreateDeviceToken(miaID, "mia-laptop")
	must(err)
	tokT, _, err := s.st.CreateDeviceToken(theoID, "theo-desktop")
	must(err)
	out["token_mia"], out["token_theo"] = tokM, tokT
	out["project"] = sf
	s.finish()
}

// ---- dataset: starter -------------------------------------------------------------

func seedStarter(path string, out map[string]string) {
	s := newSeeder(path)
	s.people(false)
	s.projects(false, sf)
	d := 24 * time.Hour
	s.requests([]reqSpec{
		{sf, "feature", "Gift-wrap option in the cart", "Customers should be able to add gift wrap with a short card message.",
			[]string{"A gift-wrap checkbox in the cart", "The card message appears on the packing slip", "The price is shown before paying"}, 1, "Mia", 2 * time.Hour},
		{sf, "change", "Shorter checkout", "Cut the checkout from five steps to three.",
			[]string{"Address and email on one page", "Payment on the second page", "Confirmation on the third"}, 0, "Mia", 1 * d},
	})
	s.registerAgents(starterAgents)
	mkv := func(typ, title, body string, age time.Duration, expl string) kn {
		return kn{sf, typ, title, body, "verified", "human", "", "Mia", age, expl}
	}
	s.knowledge([]kn{
		mkv("instruction", "Open a pull request, never push straight to main", "All changes go through a pull request, even small ones.", 5*d, "A house rule: every change is looked at by a second person first."),
		mkv("pitfall", "Prices are stored in cents", "4.90 is stored as 490. Never calculate with decimal dollars.", 5*d, "A classic trap with money: prices are whole cents, so totals stay exact."),
		mkv("note", "Deploys start when a version tag is pushed", "A tag starting with v starts the release pipeline.", 4*d, "How a release starts: with a tag, not with a button."),
		mkv("decision", "Customer emails stay under 120 words", "Nobody reads more. Short sentences, friendly tone.", 3*d, "A writing decision for every customer email: short and friendly."),
		{sf, "pitfall", "Gift messages with emoji break the packing slip printer", "The printer driver cannot render emoji. Strip them before printing and tell the customer.", "staged", "agent", "claude", "Mia", 40 * time.Minute,
			"A small trap for the new gift-wrap feature: the packing slip printer cannot print emoji."},
		{sf, "decision", "Gift wrap costs 3.50 and is shown before paying", "The price is a flat 3.50 so customers see it in the cart, never as a surprise at the end.", "staged", "agent", "claude", "Mia", 2 * time.Hour,
			"A decision made while building gift wrap, recorded with its reason."},
	})
	t := s.transcript("a1", 95*time.Minute)
	t.user("Add a gift-wrap option to the cart. A checkbox and a short card message.")
	t.say("I'll look at how the cart renders its lines.", read("src/cart/CartLines.tsx", "export function CartLines({ items }) {\n  return items.map(i => <Line key={i.id} item={i} />)\n}"))
	t.say("The cart has no extras yet. I'll add a gift-wrap row below the lines with a checkbox and a text field limited to 120 characters.",
		edit("src/cart/CartLines.tsx", "return items.map(i => <Line key={i.id} item={i} />)", "return <>{items.map(i => <Line key={i.id} item={i} />)}<GiftWrapRow /></>"))
	t.flush()
	tokM, _, err := s.st.CreateDeviceToken(miaID, "mia-laptop")
	must(err)
	out["token_mia"] = tokM
	out["project"] = sf
	s.finish()
}

// finish closes the store and applies every back-dating in one go, then fixes
// session titles. All of it goes through a second connection after Close.
func (s *seeder) finish() {
	must(s.st.Close())
	db, err := sql.Open("sqlite", "file:"+s.path+"?_pragma=busy_timeout(10000)")
	must(err)
	defer db.Close()
	for _, q := range s.postSQL {
		_, err = db.Exec(q)
		must(err)
	}
	for _, b := range s.back {
		ts := s.ago(b.age)
		switch b.table {
		case "coord_messages":
			_, err = db.Exec(`UPDATE coord_messages SET created_at=? WHERE id=?`, ts, b.id)
			must(err)
			_, err = db.Exec(`UPDATE coord_attention SET created_at=? WHERE message_id=?`, ts, b.id)
			must(err)
		case "knowledge":
			_, err = db.Exec(`UPDATE knowledge SET created_at=?, updated_at=?, observed_at=? WHERE id=?`, ts, ts, ts, b.id)
			must(err)
		case "human_tasks":
			_, err = db.Exec(`UPDATE human_tasks SET created_at=?, updated_at=? WHERE id=?`, ts, ts, b.id)
			must(err)
		case "pains":
			_, err = db.Exec(`UPDATE pains SET created_at=?, updated_at=? WHERE id=?`, ts, ts, b.id)
			must(err)
		case "requests":
			_, err = db.Exec(`UPDATE requests SET created_at=?, updated_at=? WHERE id=?`, ts, s.ago(b.age/3), b.id)
			must(err)
			_, err = db.Exec(`UPDATE request_criteria SET created_at=?, updated_at=? WHERE request_id=?`, ts, s.ago(b.age/3), b.id)
			must(err)
			_, err = db.Exec(`UPDATE request_evidence SET created_at=? WHERE request_id=?`, s.ago(b.age/2), b.id)
			must(err)
			_, err = db.Exec(`UPDATE request_activity SET created_at=? WHERE request_id=?`, s.ago(b.age/2), b.id)
			must(err)
		case "session_last":
			_, err = db.Exec(`UPDATE sessions SET started_at=?, last_seen_at=? WHERE id=?`, s.ago(s.sessStart[b.id]), s.ago(s.sessAges[b.id]), b.id)
			must(err)
		}
	}
	for ext, t := range s.titles {
		_, err = db.Exec(`UPDATE sessions SET title=? WHERE external_id=?`, t, ext)
		must(err)
	}
	fmt.Fprintln(os.Stderr, "seeded")
}

// ---- pulse -------------------------------------------------------------------------

// pulse keeps the agents that are meant to be "working" or "waiting" in that
// state while a shot or a recording runs: the app derives "working" from a hook
// event younger than two minutes and "connected" from a poll younger than 90 s.
func pulse(path string, every time.Duration) {
	st, err := store.Open(path)
	must(err)
	defer st.Close()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(every)
	defer tick.Stop()
	list := teamAgents
	if n, _ := countAgents(path); n > 0 && n <= len(starterAgents) {
		list = starterAgents
	}
	for {
		for i, a := range list {
			ext := a.ext(i + 1)
			reg := store.CoordAgent{ExternalID: ext, Provider: a.provider, RoomKey: store.RoomKeyForProject(a.project), PrincipalID: a.owner, DisplayName: ext,
				Branch: a.branch, Machine: a.machine, SessionID: "s-" + agentUUID(i+1), Cwd: a.cwd()}
			switch a.state {
			case "working":
				reg.RegisteredAt = time.Now().UTC().Format(time.RFC3339)
				reg.HookActivity, reg.HookEvent = true, store.HookTool
				if _, err := st.RegisterCoordAgent(reg); err != nil {
					fmt.Fprintln(os.Stderr, "pulse register:", err)
				}
				_ = st.TouchCoordAgentPoll(ext)
			case "waiting", "waiting_peer", "stuck":
				_ = st.TouchCoordAgentPoll(ext)
			}
		}
		select {
		case <-stop:
			os.Exit(0) // the DB is in WAL mode; closing the store is not needed and has hung
		case <-tick.C:
		}
	}
}

func countAgents(path string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM coord_agents`).Scan(&n)
	return n, err
}
