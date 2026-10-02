package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// Das generierte Schema darf nur verlangen, was wirklich Pflicht ist.
// context_search hatte einmal vier grüne Tests und war für Agenten
// unbenutzbar, weil ein optionales Feld im Schema als Pflicht stand (#846).
// Handler-Tests sehen diese Grenze nicht — deshalb liest dieser Test das
// Schema so, wie ein Client es über die Leitung bekommt.
func TestCoordToolSchemasRequireOnlyWhatIsMandatory(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{
		Project: "github.com/x/y", Machine: "testbox"}})

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		// Nur der Text ist Pflicht. Raum, Antwortbezug, Ablauf und Erwähnung
		// sind optional und müssen omitempty tragen.
		"coord_send": {"body"},
		// Beide sind vollständig optional: ein Agent soll "was gibt es
		// Neues" fragen können, ohne vorher einen Raum zu benennen.
		"coord_inbox": nil,
		"coord_peers": nil,
	}

	seen := map[string]bool{}
	for _, tool := range tools.Tools {
		expected, tracked := want[tool.Name]
		if !tracked {
			continue
		}
		seen[tool.Name] = true
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, got := range schema.Required {
			if !slices.Contains(expected, got) {
				t.Errorf("%s requires %q, which is optional — the SDK will reject calls that omit it: %s",
					tool.Name, got, raw)
			}
		}
		for _, must := range expected {
			if !slices.Contains(schema.Required, must) {
				t.Errorf("%s does not require %q", tool.Name, must)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("%s is not registered", name)
		}
	}
}

// Die Werkzeugbeschreibung ist das Einzige, was ein Modell vor dem Aufruf
// über das Werkzeug weiß. coord_send darf dort keine Zustellung versprechen,
// die niemand geprüft hat — Spec §A7.
func TestCoordSendDescriptionDoesNotPromiseDelivery(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{
		Project: "github.com/x/y", Machine: "testbox"}})

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "coord_send" {
			continue
		}
		if !strings.Contains(tool.Description, "stored, never delivered") {
			t.Errorf("coord_send does not say it reports stored rather than delivered: %q", tool.Description)
		}
		return
	}
	t.Fatal("coord_send is not registered")
}

// Ein Agent ohne Repository bekommt eine brauchbare Antwort statt eines
// leeren Raums: die Fehlermeldung nennt den Ausweg.
func TestRoomResolutionWithoutRepositoryPointsAtTheMachineRoom(t *testing.T) {
	s := &Server{ctxAxes: scope.Axes{Machine: "testbox"}}
	if _, err := s.roomKeyFor("project"); err == nil {
		t.Fatal("a session without a repository must not resolve a project room")
	} else if !strings.Contains(err.Error(), "machine") {
		t.Errorf("the error should name the way out: %v", err)
	}
	if _, err := s.roomKeyFor("machine"); err != nil {
		t.Errorf("the machine room must still work: %v", err)
	}
}

// Ein erfundener Raumname wird abgewiesen, statt still auf das Projekt zu
// fallen. Sonst landet eine maschinenweite Meldung im Projektraum.
func TestUnknownRoomIsRejected(t *testing.T) {
	s := &Server{ctxAxes: scope.Axes{Project: "github.com/x/y", Machine: "testbox"}}
	if _, err := s.roomKeyFor("global"); err == nil {
		t.Fatal("an unknown room name must be rejected rather than silently defaulted")
	}
}

// Das Schema, das ein Client über die Leitung sieht, muss intent als
// optionales Feld mit genau den Werten tragen, die der Server annimmt. Ohne
// das Feld konnte ein Agent keine Frage, Freigabe, Blockade oder Übergabe
// anlegen, obwohl die Anleitung ihn dazu auffordert.
func TestCoordSendSchemaCarriesIntentEnum(t *testing.T) {
	c, _ := newTestClient(t)
	session := connect(t, &Server{client: c, ctxAxes: scope.Axes{
		Project: "github.com/x/y", Machine: "testbox"}})
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "coord_send" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		prop, ok := schema.Properties["intent"]
		if !ok {
			t.Fatalf("coord_send has no lowercase intent property: %s", raw)
		}
		for _, want := range []string{"question", "approval", "blocker", "handoff", "ack"} {
			if !slices.Contains(prop.Enum, want) {
				t.Errorf("intent enum lacks %q: %s", want, raw)
			}
		}
		if slices.Contains(prop.Enum, "standing") {
			t.Errorf("intent enum must not offer standing: %s", raw)
		}
		return
	}
	t.Fatal("coord_send is not registered")
}
