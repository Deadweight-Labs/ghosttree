package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Kriterium 3: ein Agenten-Eintrag heißt „alice’s Claude“ mit Kennzeichen;
// Modell und Maschine stehen nur im Detail und nur für, wer Maschinen sieht, der
// Session-Link nur für, wer das Transkript lesen darf.
func TestAgentEntriesShowPersonAndHarnessWithDetailsByRights(t *testing.T) {
	e, pa := listEnv(t)
	sid, err := e.St.UpsertSession(store.Session{Harness: "claude-code", ExternalID: "s-alice", AccountID: 1, Scope: scope.Axes{Project: shellProject, Machine: "laptop"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.AppendChunks(sid, []store.Chunk{{Seq: 0, Role: "assistant", Raw: `{"type":"assistant","timestamp":"2020-01-01T00:00:00Z","message":{"model":"claude-opus-5-5"}}`}}); err != nil {
		t.Fatal(err)
	}
	byAgent, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Agent pain", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.Pain, byAgent.Pain.ID, actor.Actor{Kind: actor.Agent, AccountID: 1, Harness: "claude-code", Machine: "laptop", SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	byHand, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Hand pain", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.Pain, byHand.Pain.ID, actor.Actor{Kind: actor.Human, AccountID: 1}); err != nil {
		t.Fatal(err)
	}
	page := func(c *http.Client, id int64) string {
		t.Helper()
		code, body := fetchPage(t, c, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(id, 10))
		if code != http.StatusOK {
			t.Fatalf("pain page %d: %d", id, code)
		}
		return body
	}
	owner := page(e.Owner, byAgent.Pain.ID)
	for _, want := range []string{"alice’s Claude", `class="lb out botmark">agent<`, `class="actor-d">Opus 5.5 · laptop<`, `href="/ui/sessions/`} {
		if !strings.Contains(owner, want) {
			t.Errorf("owner's page lacks %q", want)
		}
	}
	// Eine private Session sieht nur ihr Besitzer: ein Mitglied bekommt den
	// Agenten, aber weder Modell noch Maschine noch Link.
	member := page(e.Member, byAgent.Pain.ID)
	if !strings.Contains(member, "alice’s Claude") || strings.Contains(member, "Opus 5.5") || strings.Contains(member, "laptop") {
		t.Error("a member sees model or machine of a private session, or not the agent")
	}
	if strings.Contains(member, `<a href="/ui/sessions/`) {
		t.Error("a member gets the link to someone else's private session")
	}
	// Freigegeben fürs Projekt: jetzt sieht das Mitglied Modell und Maschine.
	if err := e.St.SetSessionShared(sid, "person:1", true); err != nil {
		t.Fatal(err)
	}
	if shared := page(e.Member, byAgent.Pain.ID); !strings.Contains(shared, "Opus 5.5 · laptop") {
		t.Error("a member does not see model and machine of a shared session")
	}
	// Ohne Session steht nur die Maschine des Gerätetokens, und die nur für ihr Konto.
	bare, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Bare pain", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.Pain, bare.Pain.ID, actor.Actor{Kind: actor.Agent, AccountID: 1, Machine: "devbox"}); err != nil {
		t.Fatal(err)
	}
	if own := page(e.Owner, bare.Pain.ID); !strings.Contains(own, `class="actor-d">devbox<`) {
		t.Error("the owner does not see the machine of the device token")
	}
	if other := page(e.Member, bare.Pain.ID); strings.Contains(other, "devbox") {
		t.Error("a member sees the machine of someone else's device token")
	}
	guest := page(e.Guest, byAgent.Pain.ID)
	if !strings.Contains(guest, "alice’s Claude") || strings.Contains(guest, "Opus 5.5") || strings.Contains(guest, "laptop") {
		t.Error("a guest sees model or machine, or not the agent")
	}
	// In der Liste nur der Name, ohne Modell und Maschine.
	_, list := fetchPage(t, e.Member, e.Base+"/ui/x/shell/issues?q="+url.QueryEscape("is:pain is:open"))
	if !strings.Contains(list, "<span>alice’s Claude</span>") || strings.Contains(list, "Opus 5.5") {
		t.Error("the pain list does not name the agent, or shows its model")
	}
	hand := page(e.Owner, byHand.Pain.ID)
	if strings.Contains(hand, "botmark") || strings.Contains(hand, "’s Claude") {
		t.Error("a person's entry is shown as an agent")
	}
}

func TestPossessive(t *testing.T) {
	for _, c := range []struct {
		l          lang
		name, want string
	}{{langDE, "Robin", "Robins"}, {langDE, "Hans", "Hans’"}, {langDE, "Max", "Max’"}, {langEN, "Robin", "Robin’s"}, {langEN, "James", "James’"}} {
		if got := possessive(c.l, c.name); got != c.want {
			t.Errorf("possessive(%s, %q) = %q, want %q", c.l, c.name, got, c.want)
		}
	}
}

// Kriterium 5 im Web: die Seite Modelle zeigt je Harness und Modell die Zahlen.
func TestAgentModelsPage(t *testing.T) {
	e, pa := listEnv(t)
	sid, err := e.St.UpsertSession(store.Session{Harness: "codex", ExternalID: "s-codex", AccountID: 1, Scope: scope.Axes{Project: shellProject}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.AppendChunks(sid, []store.Chunk{{Seq: 0, Raw: `{"timestamp":"2020-01-01T00:00:00Z","type":"turn_context","payload":{"model":"gpt-6-sol"}}`}}); err != nil {
		t.Fatal(err)
	}
	d, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Counted", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.Pain, d.Pain.ID, actor.Actor{Kind: actor.Agent, AccountID: 1, Harness: "codex", SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	code, body := fetchPage(t, e.Owner, e.Base+"/ui/agents/models")
	if code != http.StatusOK {
		t.Fatalf("models page: %d", code)
	}
	for _, want := range []string{`class="mstats"`, "<span>Codex</span>", `title="gpt-6-sol"`, `href="/ui/agents/models"`} {
		if !strings.Contains(body, want) {
			t.Errorf("models page lacks %q", want)
		}
	}
	if _, guest := fetchPage(t, e.Guest, e.Base+"/ui/agents/models"); strings.Contains(guest, "gpt-6-sol") {
		t.Error("a guest sees the model statistics of the project")
	}
}

// Ein Mensch im Terminal steht mit seinem Namen und dem Kennzeichen „terminal“,
// in der Statistik als eigene Zeile.
func TestTerminalPersonShowsAsSuch(t *testing.T) {
	e, pa := listEnv(t)
	d, err := pa.CreatePain(store.CreatePainInput{Project: shellProject, Title: "Typed by hand", Actor: pain.Actor{AccountID: 1, Person: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.St.RecordActor(actor.Pain, d.Pain.ID, actor.Actor{Kind: actor.Human, AccountID: 1, Via: actor.ViaTerminal}); err != nil {
		t.Fatal(err)
	}
	_, page := fetchPage(t, e.Owner, e.Base+"/ui/x/shell/pains/"+strconv.FormatInt(d.Pain.ID, 10))
	if !strings.Contains(page, `class="lb out botmark" title="self-reported by ctx in a terminal">terminal<`) || strings.Contains(page, "’s Claude") {
		t.Error("the pain page does not mark the terminal person")
	}
	_, stats := fetchPage(t, e.Owner, e.Base+"/ui/agents/models")
	if !strings.Contains(stats, "People (terminal)") {
		t.Error("the models page has no line for people in the terminal")
	}
}
