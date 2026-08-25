package mcpserver

import (
	"reflect"
	"strings"
	"testing"
)

// Ein Feld im Schema ist ein Versprechen. response_format stand in
// RequestSearchInput und wurde vom Handler nie gelesen — ein Codex-Prüflauf hat
// am 2026-08-25 "detailed" angefordert, dieselbe knappe Ausgabe bekommen und es
// zu Recht als wirkungslose öffentliche Option gemeldet.
//
// Entfernt statt implementiert, weil die ausführliche Variante schon einmal
// gebaut und wieder abgeschafft wurde: 24 volle Beschreibungen kamen auf 64.462
// Zeichen und sprengten das Werkzeuglimit (REQ-166). Wer den ganzen Text will,
// ruft request_get.
func TestRequestSearchDoesNotOfferAnOptionItIgnores(t *testing.T) {
	if _, ok := reflect.TypeOf(RequestSearchInput{}).FieldByName("ResponseFormat"); ok {
		t.Error("request_search still advertises response_format but ignores it")
	}
	// request_get liest es und behält es deshalb.
	if _, ok := reflect.TypeOf(RequestGetInput{}).FieldByName("ResponseFormat"); !ok {
		t.Error("request_get uses response_format and must keep offering it")
	}
}

// Eine Trefferliste, die genau am Limit endet, sieht vollständig aus. Dasselbe
// Muster wie bei null Treffern (#732): der Leser schliesst aus der Form auf den
// Bestand und hört auf zu fragen. Gefunden am 2026-08-25 von einem
// Codex-Prüflauf: "Die Suche kann gleichzeitig vollständig aussehen und
// unvollständig sein."
func TestASearchThatHitsItsLimitSaysSo(t *testing.T) {
	if note := limitNote(searchLimit, searchLimit); note == "" {
		t.Error("a section cut at the limit must say that more may exist")
	} else if !strings.Contains(note, "context_search") {
		t.Errorf("the note must say how to get further: %q", note)
	}
	if note := limitNote(3, searchLimit); note != "" {
		t.Errorf("a short section must not warn about a limit it did not reach: %q", note)
	}
}
