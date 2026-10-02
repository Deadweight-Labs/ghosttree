package codexadapter

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func needCodex(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not installed")
	}
	if testing.Short() {
		t.Skip("talks to a real codex app server")
	}
}

// Die Fähigkeitsliste ist eine Aussage über MESSUNGEN, nicht über
// Protokollmethoden. Spec §A5: "MCP verbunden" genügt nicht als
// Live-Zertifikat — und ein Adapter, der pauschal "kann live" meldet,
// verbirgt die Lücke, statt sie zu benennen.
func TestCapabilitiesClaimOnlyWhatWasMeasured(t *testing.T) {
	caps := map[string]bool{}
	for _, c := range Capabilities() {
		caps[c] = true
	}
	if !caps[CapWakeIdleSession] {
		t.Error("waking an idle session is measured and must be claimed")
	}
	// Das ist der Punkt: Push in einen laufenden fremden Turn ist NICHT
	// gemessen und darf nicht behauptet werden.
	if caps[CapReceiveAtSafePoint] {
		t.Error("delivery into a running foreign turn is not measured and must not be claimed")
	}
	if caps[CapHumanPause] || MissingCapabilities()[CapHumanPause] == "" {
		t.Error("a Codex pause is not measured: it must be a named gap")
	}
	if caps[CapHumanInterrupt] {
		t.Error("interrupting is untested here; claiming it would be the thing this package refuses to do")
	}

	missing := MissingCapabilities()
	for _, c := range []string{CapReceiveAtSafePoint, CapReceiveForSubagent, CapHumanSteer, CapHumanInterrupt} {
		if reason := missing[c]; reason == "" {
			t.Errorf("%s is missing without a stated reason — a gap without a reason reads like an oversight", c)
		}
	}
}

// Der Adapter redet mit einem echten App Server. Ohne diesen Test wäre die
// ganze Zustellbehauptung wieder das, was sie vorher war: eine Annahme.
func TestThreadsComeFromARealAppServer(t *testing.T) {
	needCodex(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Dial(ctx)
	if err != nil {
		t.Skipf("no usable codex app server here: %v", err)
	}
	defer c.Close()

	threads, err := c.Threads(3)
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	if len(threads) == 0 {
		t.Skip("no codex sessions on this machine to list")
	}
	for _, th := range threads {
		if th.ID == "" {
			t.Fatalf("a thread without an id is not addressable: %+v", th)
		}
	}
}

// Eine leere Zustellung wird abgelehnt, bevor irgendein Prozess startet.
func TestDeliveryNeedsBothThreadAndText(t *testing.T) {
	var c Client
	if _, err := c.WakeIdleThread("", "hallo", time.Second); err == nil {
		t.Error("a delivery without a thread must be rejected")
	}
	if _, err := c.WakeIdleThread("abc", "", time.Second); err == nil {
		t.Error("a delivery without text must be rejected")
	}
}

// Der Antwortleser unterscheidet Modelltext von Statusereignissen. Ein
// Adapter, der das erste Beste für die Antwort hält, liest irgendwann eine
// fremde Statusmeldung als Erfolg.
func TestOnlyAgentTextCountsAsAReply(t *testing.T) {
	cases := []struct{ params, want string }{
		{`{"item":{"type":"agent_message","text":"Ja, items bleibt."}}`, "Ja, items bleibt."},
		{`{"item":{"role":"assistant","text":"auch eine Antwort"}}`, "auch eine Antwort"},
		{`{"item":{"type":"reasoning","text":"nur Denken"}}`, ""},
		{`{"status":"disabled"}`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := agentText([]byte(c.params)); got != c.want {
			t.Errorf("agentText(%s) = %q, want %q", c.params, got, c.want)
		}
	}
}

// Angenommen heißt "der Harness hat die Eingabe genommen" — nicht "das Modell
// hat sie befolgt". Die Unterscheidung steht im Typ, damit sie nicht in der
// Übersetzung verlorengeht.
func TestAcceptedIsNotObeyed(t *testing.T) {
	out := DeliveryOutcome{Accepted: true}
	if out.Reply != "" {
		t.Error("an accepted delivery carries no reply until one was observed")
	}
	if !strings.Contains(MissingCapabilities()[CapReceiveAtSafePoint], "started the turn") {
		t.Error("the reason for the missing push capability must name the ownership limit")
	}
}
