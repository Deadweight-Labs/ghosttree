package store

import (
	"regexp"
	"strings"
	"unicode"
)

// Das Urteil über eine Zustellung (REQ-645, P3 des Konzepts "Schlankes Profil und
// Kontext, der hilft", Teil 2): Hat der Agent einen zugestellten Eintrag danach
// erkennbar benutzt?
//
// Der Matcher ist rein. Er bekommt die Texte des Transkripts Zeile für Zeile (so,
// wie chunktext sie liefert) und eine Liste von Dingen, die zugestellt wurden,
// und sagt je Ding 0, 1 oder 2. Er kennt weder die Datenbank noch Konten; was er
// liest, behält er nicht, und sein Ergebnis besteht aus Zahlen:
//
//	UsedNone     0  nichts davon kommt im Tun des Agenten vor
//	UsedPickedUp 1  S1 aufgegriffen: Nummer, zwei unterscheidende Wörter des Titels
//	               oder ein gezielter Abruf des Eintrags
//	UsedFollowed 2  S2 gefolgt: der im Eintrag genannte Pfad kommt in einem
//	               Werkzeugaufruf vor, der genannte Befehl wird ausgeführt, bei
//	               einem Ablauf folgt der Bericht
//
// "Im Tun des Agenten" heißt: Text des Assistenten und die Aufrufe seiner
// Werkzeuge. Nicht dazu gehören Nachrichten des Menschen und Ergebnisse von
// Werkzeugen, denn dort steht der zugestellte Text selbst (ein Suchtreffer
// nennt den Eintrag, ohne dass jemand ihn benutzt hätte). Gedankentext liefert
// chunktext nicht.
//
// Das Signal ist ein Hinweis und kein Beweis; S1 misst im ungünstigen Fall
// Wortgleichheit. Deshalb folgt aus ihm nichts, bevor eine Prüfstichprobe es
// belegt (P5, DEC-14).

// Die Werte von context_delivery_log.used.
const (
	UsedPending  = -1
	UsedNone     = 0
	UsedPickedUp = 1
	UsedFollowed = 2
)

// JudgeableKind sagt, ob für diese Art von Zustellung ein Urteil möglich ist.
// Pfadbeschreibungen, Hinweiszeilen, Arbeitsbuch-Abschnitte, Feature-Kontext und
// der Rahmen haben keine Nummer und keinen Titel, an dem sich ein Aufgreifen
// ablesen ließe; für die Pfadbeschreibungen nennt das Konzept eine eigene
// Kennzahl (erneutes Lesen der Datei), die nicht Teil von P3 ist. Ihre Zeilen
// bleiben auf -1 und gelten im Bericht als nicht beurteilt.
func JudgeableKind(kind string) bool {
	return kind == "knowledge" || kind == "runbook"
}

// judgeItem ist ein zugestelltes Ding, wie der Matcher es braucht.
type judgeItem struct {
	Key   int64 // vom Aufrufer, kommt im Ergebnis zurück
	Kind  string
	ID    int64
	Since string // Zustellzeit, auf die Sekunde: nur spätere Zeilen zählen
	Title string // bei einem Ablauf der Name
	Body  string
}

// judgeLine ist eine Zeile des Transkripts: Rolle und Text nach chunktext, dazu
// ihre Zeit (leer, wenn der Index sie nicht kennt).
type judgeLine struct {
	Role, Text, At string
}

// minTitleWords ist die Zahl unterscheidender Titelwörter, die zusammen als
// Aufgreifen gelten. Eines allein ist ein Zufall (das Wort kommt in jeder
// Sitzung über dieses Thema vor).
const minTitleWords = 2

// minRunbookName hält kurze Namen heraus, die auch ein gewöhnliches Wort sein
// könnten ("build", "deploy"). Ein Name mit Bindestrich ("rollout-prod") ist
// keines und darf kürzer sein.
const minRunbookName = 8

var (
	// Eine Nummer: KN-4251, #4251, knowledge/4251 (Adresse einer Wissensseite)
	// oder ein Abruf mit knowledge_id (JSON oder Befehlszeile). Davor und danach
	// keine Ziffer.
	idPattern = regexp.MustCompile(`(?i)(?:\bkn-|#|/knowledge/|knowledge_id["']?\s*[:=]\s*["']?)(\d{1,9})\b`)
	// Ein Pfad mit Verzeichnis und einer Endung, die ein Buchstabe einleitet.
	pathPattern = regexp.MustCompile("[A-Za-z0-9_.@~-]+(?:/[A-Za-z0-9_.@-]+)+\\.[A-Za-z][A-Za-z0-9]{0,7}\\b")
	urlPattern  = regexp.MustCompile(`[a-z][a-z0-9+.-]*://\S+`)
	// Ein Befehl im Fließtext: Text zwischen Gravis, ohne Zeilenumbruch.
	codeSpan = regexp.MustCompile("`([^`\\n]{4,160})`")
	// Platzhalter in einem Befehl: ab hier ist es Muster, nicht mehr Wortlaut.
	placeholder = regexp.MustCompile(`[<\[{$]|\.\.\.|…`)
	cmdWord     = regexp.MustCompile(`^[a-z][a-z0-9.-]*$`)
)

// judgeTarget ist ein Ding mit dem, was der Matcher davon wissen muss.
type judgeTarget struct {
	item   judgeItem
	words  []string // unterscheidende Wörter des Titels
	name   string   // kleingeschriebener Name eines Ablaufs
	paths  []string
	cmds   []string
	result int
}

// judger prüft Zeilen gegen Dinge. Dinge mit derselben Nummer teilen sich keine
// Zustände, jedes bekommt sein Urteil.
type judger struct {
	targets []*judgeTarget
	byID    map[int64][]*judgeTarget // Wissenseinträge nach Nummer
}

func newJudger(items []judgeItem) *judger {
	j := &judger{byID: map[int64][]*judgeTarget{}}
	for _, it := range items {
		t := &judgeTarget{item: it}
		switch it.Kind {
		case "knowledge":
			t.words = distinctiveTerms(it.Title)
			if len(t.words) > 2*minTitleWords+4 {
				t.words = t.words[:2*minTitleWords+4]
			}
			t.paths = pathsIn(it.Body)
			t.cmds = commandsIn(it.Body)
			j.byID[it.ID] = append(j.byID[it.ID], t)
		case "runbook":
			n := strings.ToLower(strings.TrimSpace(it.Title))
			if len(n) >= minRunbookName || len(n) >= 5 && strings.Contains(n, "-") {
				t.name = n
			}
		}
		j.targets = append(j.targets, t)
	}
	return j
}

// pathsIn liefert die Pfade, die ein Eintragstext nennt, ohne Wiederholungen
// und ohne Adressen.
func pathsIn(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range pathPattern.FindAllString(urlPattern.ReplaceAllString(body, " "), -1) {
		p = strings.TrimPrefix(p, "./")
		if len(p) < 6 || strings.Contains(p, "://") || strings.HasPrefix(p, "~") || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) == 12 {
			break
		}
	}
	return out
}

// commandsIn liefert die Befehle, die ein Eintragstext in Gravis nennt: mindestens
// zwei Wörter, das erste ein Programmname (kleingeschrieben, ohne Unterstrich und
// ohne Pfad). Ein Platzhalter schneidet den Befehl ab, es bleibt der Teil davor.
func commandsIn(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range codeSpan.FindAllStringSubmatch(body, -1) {
		c := m[1]
		if loc := placeholder.FindStringIndex(c); loc != nil {
			c = c[:loc[0]]
		}
		fields := strings.Fields(c)
		if len(fields) < 2 || !cmdWord.MatchString(fields[0]) {
			continue
		}
		c = strings.Join(fields, " ")
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) == 8 {
			break
		}
	}
	return out
}

// agentAct sagt, ob die Zeile etwas ist, das der Agent getan oder gesagt hat,
// und ob es ein Werkzeugaufruf ist. Claude schreibt die Aufrufe in die Zeile des
// Assistenten, Codex in eine eigene Zeile mit Rolle tool und der Marke
// "[tool call: ".
func agentAct(role, text string) (act, call bool) {
	switch {
	case role == "assistant":
		return true, strings.Contains(text, "[tool call: ")
	case role == "tool" && strings.HasPrefix(text, "[tool call: "):
		return true, true
	}
	return false, false
}

// feed prüft eine Zeile. Zeilen, die nicht von der Zustellung an gelten, werden
// je Ding übersprungen: at und Since sind Zeiten auf die Sekunde, es zählt, was
// strikt danach steht (der Aufruf, der die Zustellung auslöste, steht in
// derselben Sekunde davor). Ohne Zeit zählt die Zeile.
func (j *judger) feed(l judgeLine) {
	act, call := agentAct(l.Role, l.Text)
	if !act {
		return
	}
	ids := idsIn(l.Text)
	var words map[string]bool
	var lower string
	for _, t := range j.targets {
		if t.result == UsedFollowed || !after(l.At, t.item.Since) {
			continue
		}
		switch t.item.Kind {
		case "knowledge":
			if call && t.followed(l.Text) {
				t.result = UsedFollowed
				continue
			}
			if t.result >= UsedPickedUp {
				continue
			}
			if ids[t.item.ID] {
				t.result = UsedPickedUp
				continue
			}
			if len(t.words) >= minTitleWords {
				if words == nil {
					words = termSet(l.Text)
				}
				n := 0
				for _, w := range t.words {
					if words[w] {
						n++
					}
				}
				if n >= minTitleWords {
					t.result = UsedPickedUp
				}
			}
		case "runbook":
			if t.name == "" {
				continue
			}
			if lower == "" {
				lower = strings.ToLower(l.Text)
			}
			if !containsName(lower, t.name) {
				continue
			}
			if call && (strings.Contains(lower, "runbook report") || strings.Contains(lower, `"action":"report"`) ||
				strings.Contains(lower, "ctx run ")) {
				t.result = UsedFollowed
			} else if t.result < UsedPickedUp {
				t.result = UsedPickedUp
			}
		}
	}
}

// followed: ein Werkzeugaufruf nennt einen Pfad oder führt einen Befehl aus, den
// der Eintrag nennt.
func (t *judgeTarget) followed(callText string) bool {
	for _, p := range t.paths {
		if strings.Contains(callText, p) {
			return true
		}
	}
	if len(t.cmds) > 0 {
		flat := strings.Join(strings.Fields(callText), " ")
		for _, c := range t.cmds {
			if strings.Contains(flat, c) {
				return true
			}
		}
	}
	return false
}

// verdicts liefert je Ding sein Urteil, in der Reihenfolge der Eingabe.
func (j *judger) verdicts() map[int64]int {
	out := make(map[int64]int, len(j.targets))
	for _, t := range j.targets {
		out[t.item.Key] = t.result
	}
	return out
}

// after sagt, ob eine Zeile von der Zustellung an zählt. Beide Zeiten sind ISO
// 8601 in UTC; verglichen wird auf die Sekunde, weil die Zustellzeit keine
// Bruchteile hat und der Zeitstempel der Zeile welche.
func after(at, since string) bool {
	if len(at) < 19 || len(since) < 19 {
		return true
	}
	return at[:19] > since[:19]
}

// idsIn sammelt die Nummern, die ein Text nennt.
func idsIn(text string) map[int64]bool {
	var out map[int64]bool
	for _, m := range idPattern.FindAllStringSubmatch(text, -1) {
		var n int64
		for _, c := range m[1] {
			n = n*10 + int64(c-'0')
		}
		if out == nil {
			out = map[int64]bool{}
		}
		out[n] = true
	}
	return out
}

// termSet zerlegt einen Text wie distinctiveTerms (Buchstaben und Ziffern,
// kleingeschrieben), ohne die Wörter zu zählen, die dort ausgeschlossen sind:
// der Vergleich geschieht nur gegen Titelwörter, die schon gefiltert sind.
func termSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(f) >= minTermLen {
			out[f] = true
		}
	}
	return out
}

// containsName sucht einen Namen als eigenes Wort: davor und danach weder
// Buchstabe, Ziffer noch Bindestrich, damit "rollout-prod" nicht in
// "rollout-prod-test" gefunden wird.
func containsName(lower, name string) bool {
	for from := 0; ; {
		i := strings.Index(lower[from:], name)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(name)
		before := start == 0 || !nameChar(rune(lower[start-1]))
		afterOK := end >= len(lower) || !nameChar(rune(lower[end]))
		if before && afterOK {
			return true
		}
		from = start + 1
	}
}

func nameChar(r rune) bool {
	return r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
