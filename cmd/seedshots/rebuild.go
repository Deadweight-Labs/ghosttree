package main

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// seedRebuild füllt eine leere Datenbank so, wie eine Instanz von zwei Entwicklern
// mit vielen Agenten in fünf Projekten nach ein paar Wochen aussieht: Agenten in
// jedem Arbeitszustand, offene Fragen von Agenten an Menschen und an Agenten,
// Aufgaben mit langen Texten, Wissensvorschläge von Agenten und vom Gärtner,
// Pains. Alle Zeiten sind relativ zum Startzeitpunkt; wer "Working" sehen will,
// fotografiert innerhalb von zwei Minuten nach dem Seeden.
func seedRebuild(path string) map[string]string {
	st, err := store.Open(path)
	must(err)
	out := map[string]string{}
	t0 := time.Now().UTC()
	ago := func(d time.Duration) string { return t0.Add(-d).Format(time.RFC3339) }

	for i, n := range []string{"Robin", "Philipp"} {
		_, err := st.AddAccount(n, "", i == 0)
		must(err)
	}
	org, err := st.CreateOrg("person:1", "Deadweight Labs", "deadweight-labs")
	must(err)
	code, _, err := st.CreateInvitation("person:1", org.ID, "", store.OrgMember, 0)
	must(err)
	_, err = st.AcceptInvitation("person:2", code)
	must(err)

	const (
		gt   = "github.com/deadweight-labs/ghosttree"
		pw   = "github.com/deadweight-labs/pixelworks"
		ar   = "github.com/deadweight-labs/arkon"
		ds   = "github.com/deadweight-labs/dwl-design-system"
		infr = "github.com/deadweight-labs/infra"
	)
	projects := []string{gt, pw, ar, ds, infr}
	for _, p := range projects {
		_, err := st.EnsureProject("person:1", p)
		must(err)
		must(st.SetProjectRole("person:1", p, "person:2", store.RoleMember, false, store.RoleViaAPI))
	}
	st.SetAccessMode(store.AccessMode{Enforce: true})

	robinP := store.Principal{ID: "person:1", Label: "Robin"}
	philP := store.Principal{ID: "person:2", Label: "Philipp"}
	robinWeb := store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind}
	principal := map[string]store.Principal{"person:1": robinP, "person:2": philP}
	robin := st.Access(robinP)
	phil := st.Access(philP)
	robinActor := humantask.Actor{AccountID: 1, Person: "Robin"}
	philActor := humantask.Actor{AccountID: 2, Person: "Philipp"}

	// ---- Requests ---------------------------------------------------------
	type reqSpec struct {
		project, typ, title, desc string
		criteria                  []string
		met                       int
		owner                     string
	}
	reqSpecs := []reqSpec{
		{gt, "feature", "Review-Seite: Vorschläge gebündelt prüfen", "Die Review-Liste hat 26 Einträge ohne Gruppierung. Vorschläge sollen nach Projekt und Herkunft zusammengefasst und gesammelt bestätigt oder verworfen werden können.", []string{"Vorschläge nach Projekt gruppiert", "Sammelaktion bestätigen und verwerfen", "Gärtner-Vorschläge als eine Gruppe", "Alte Vorschläge (über 7 Tage) gedämpft"}, 1, "Robin"},
		{gt, "change", "Gärtner: Rotation und einstellbare Grenzen je Job", "Der nächtliche Gärtner soll Projekte reihum prüfen und pro Job eigene Token-Grenzen haben.", []string{"Rotation über Projekte", "Grenzen je Job einstellbar", "Cache-Tokens zählen mit"}, 3, "Robin"},
		{pw, "feature", "Prospects im Admin: Liste, Filter, CSV-Export", "Vertrieb braucht im Admin eine Liste der Interessenten mit Filter nach Status und Quelle und einen Export für Excel.", []string{"Liste mit Cursor-Paginierung", "Filter Status und Quelle", "CSV-Export mit Semikolon und BOM", "Berechtigung nur für Rolle Vertrieb"}, 1, "Philipp"},
		{pw, "feature", "Stripe-Abrechnung anbinden", "Abos über Stripe abrechnen, Webhooks für Zahlungseingang und Kündigung.", []string{"Checkout-Session anlegen", "Webhook prüft Signatur", "Kündigung setzt Laufzeitende", "Live-Schlüssel im Vault"}, 2, "Robin"},
		{ar, "bug", "Import-Job bricht bei 50.000 Zeilen ohne Meldung ab", "Der nächtliche Import endet bei großen Dateien ohne Fehler im Log; es fehlen Zeilen.", []string{"Ursache gefunden", "Import läuft in Blöcken zu 5.000", "Fortschritt im Log sichtbar"}, 1, "Philipp"},
		{ar, "change", "Schema in den neuen Store migrieren", "Das alte Schema (v3) wird in den neuen Store überführt, mit Trockenlauf.", []string{"Trockenlauf-Modus", "Migration 0042 mit Backfill", "Rollback getestet"}, 0, "Robin"},
		{ds, "feature", "Tokens für den Dunkelmodus", "Semantische Farb-Tokens bekommen eine dunkle Variante, geprüft auf Kontrast.", []string{"Dunkle Variante je Token", "Kontrast AA nachgewiesen", "Umschalter in der Doku"}, 2, "Robin"},
		{infr, "change", "Backups auf die Hetzner Storage Box umstellen", "Nächtliche Sicherungen der Datenbanken wandern von lokalen Platten auf eine Storage Box, verschlüsselt mit restic.", []string{"restic-Repository angelegt", "Cron und Aufbewahrung 14 Tage", "Wiederherstellung einmal geprobt"}, 0, "Robin"},
	}
	reqID := map[int]int64{}
	reqNum := map[int]int64{}
	for i, r := range reqSpecs {
		d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: r.typ, Title: r.title, Description: r.desc, Scope: scope.Axes{Project: r.project}, Person: r.owner, Priority: []string{"hoch", "mittel", "mittel", "mittel"}[i%4]}, Criteria: r.criteria})
		must(err)
		reqID[i] = d.Request.ID
		reqNum[i] = d.Request.ID
		for j := 0; j < r.met && j < len(d.Criteria); j++ {
			must(st.SetCriterionState(d.Criteria[j].ID, "met", requestdomain.Evidence{Kind: "commit", Ref: "b" + strconv.Itoa(i) + "f3a" + strconv.Itoa(j) + "c1", Person: r.owner}))
		}
	}

	// ---- Agenten ----------------------------------------------------------
	type agentSpec struct {
		key, provider, owner, project, branch, machine, note string
		req                                                  int // Index in reqSpecs, -1 = keine
		title                                                string
		state                                                string // working, idle, stuck, offline, waiting
		last                                                 time.Duration
	}
	uuid := func(n int) string { return fmt.Sprintf("%08x-5e1f-4c0a-9b7d-%012x", 0x1a2b3c00+n, 0x7f00aa000000+n) }
	agents := []agentSpec{
		{"a1", "claude", "person:1", gt, "feat/review-bundles", "robin-arch", "Review-Seite: Gruppierung nach Projekt und Herkunft", 0, "Review-Karten gruppieren und Sammelaktion bauen", "working", 20 * time.Second},
		{"a2", "claude", "person:1", gt, "feat/garden-rotation", "robin-arch", "", 1, "Gärtner-Rotation und Grenzen je Job", "idle", 3 * time.Hour},
		{"a3", "claude", "person:1", pw, "feat/migration-0042", "robin-arch", "Migration 0042: prospects.source NOT NULL", 3, "Stripe-Webhooks und Migration 0042", "waiting", 2*time.Hour + 10*time.Minute},
		{"a4", "codex", "person:1", pw, "chore/seed-data", "robin-arch", "", -1, "Seed-Daten für die Staging-Umgebung", "idle", 5 * time.Hour},
		{"a5", "claude", "person:1", ar, "fix/import-blocks", "robin-arch", "Import in Blöcken zu 5.000 Zeilen", 4, "Import-Job: Abbruch bei 50.000 Zeilen", "stuck", 34 * time.Minute},
		{"a6", "claude", "person:1", ds, "feat/dark-tokens", "robin-arch", "Dunkle Token-Varianten, Kontrast prüfen", 6, "Dunkelmodus-Tokens", "working", 45 * time.Second},
		{"a7", "claude", "person:2", pw, "feat/prospects-admin", "philipp-mbp", "Prospects-Tabelle mit Filter und Export", 2, "Prospects im Admin: Tabelle und Filter", "working", 15 * time.Second},
		{"a8", "claude", "person:2", pw, "feat/prospects-api", "philipp-mbp", "Endpunkt /api/prospects mit Cursor", 2, "Prospects im Admin: API", "waiting_peer", 40 * time.Minute},
		{"a9", "claude", "person:2", ar, "fix/import-timezone", "philipp-mbp", "Zeitzonen im Import", 4, "Import: Zeitzone ohne Offset", "waiting", 70 * time.Minute},
		{"a10", "codex", "person:2", infr, "chore/restic", "philipp-mbp", "", 7, "restic-Repository auf der Storage Box", "idle", 7 * time.Hour},
		{"a11", "claude", "person:1", infr, "main", "apps-vm", "", -1, "Prod-Rollout prüfen", "offline", 3 * 24 * time.Hour},
	}
	extID := map[string]string{}
	sessID := map[string]int64{}
	for i, a := range agents {
		ext := fmt.Sprintf("%s:%s:%s", a.provider, a.machine, uuid(i+1))
		extID[a.key] = ext
		sess := "s-" + uuid(i+1)
		room := store.RoomKeyForProject(a.project)
		reg := store.CoordAgent{ExternalID: ext, Provider: a.provider, RoomKey: room, PrincipalID: a.owner, DisplayName: ext, Branch: a.branch, Machine: a.machine, SessionID: sess, Cwd: "/home/" + map[string]string{"person:1": "main", "person:2": "philipp"}[a.owner] + "/Projects/" + a.project[len("github.com/deadweight-labs/"):]}
		acc := int64(1)
		if a.owner == "person:2" {
			acc = 2
		}
		sid, err := st.UpsertSession(store.Session{Harness: a.provider, ExternalID: sess, Scope: scope.Axes{Project: a.project, Branch: a.branch, Machine: a.machine}, CWD: reg.Cwd, AccountID: acc})
		must(err)
		sessID[a.key] = sid
		// Erst ein beendeter Turn (damit "hängt" eine Aussage ist), dann der letzte Hook.
		first := reg
		first.RegisteredAt = ago(a.last + 90*time.Minute)
		first.HookActivity, first.HookEvent = true, store.HookTurnEnd
		_, err = st.RegisterCoordAgent(first)
		must(err)
		last := reg
		last.RegisteredAt = ago(a.last)
		last.HookActivity = true
		switch a.state {
		case "working", "stuck":
			last.HookEvent = store.HookTool
		default:
			last.HookEvent = store.HookTurnEnd
		}
		_, err = st.RegisterCoordAgent(last)
		must(err)
		if a.state != "offline" && a.state != "idle" {
			must(st.TouchCoordAgentPoll(ext))
		}
		if a.note != "" {
			_, err = st.SetCoordWorkNote(ext, a.owner, a.note)
			must(err)
		}
		if a.req >= 0 {
			_, _, err = st.StartRequestWork(reqID[a.req], sid, "primary", map[string]string{"person:1": "Robin", "person:2": "Philipp"}[a.owner])
			must(err)
		}
	}

	// ---- Räume: Nachrichten ----------------------------------------------
	var backdate []struct {
		id  int64
		age time.Duration
	}
	cid := 0
	send := func(from string, to string, project string, body string, intent string, mention []string, replyTo int64, age time.Duration, expl string) int64 {
		cid++
		room := store.RoomKeyForProject(project)
		m := store.CoordMessage{DestinationKind: store.DestinationRoom, DestinationID: room, ClientID: fmt.Sprintf("seed-%d", cid), Body: body, Intent: intent, Mentions: mention, ReplyTo: replyTo, Explanation: expl}
		var id int64
		var err error
		switch {
		case from == "robin":
			id, err = st.CoordinationFor(robinWeb, "").Send(m)
		case from == "philipp":
			id, err = st.CoordinationFor(store.Principal{ID: "person:2", Label: "Philipp", TokenKind: store.WebSessionKind}, "").Send(m)
		default:
			m.SenderExternalID = extID[from]
			owner := "person:1"
			for _, ag := range agents {
				if ag.key == from {
					owner = ag.owner
				}
			}
			id, err = st.CoordinationFor(principal[owner], extID[from]).Send(m)
		}
		must(err)
		if expl != "" {
			must(st.SaveAgentExplanation("question", id, expl))
		}
		backdate = append(backdate, struct {
			id  int64
			age time.Duration
		}{id, age})
		return id
	}
	r1 := "person:1"

	// pixelworks
	send("a7", "", pw, "Starte mit der Prospects-Liste: Tabelle, Filter nach Status und Quelle. Die API baut a8, ich nehme an, sie liefert `{items, next_cursor}`.", "", nil, 0, 3*time.Hour+20*time.Minute, "")
	q := send("a8", "", pw, "Soll /api/prospects mit Cursor oder mit Offset paginieren? Die Tabelle springt sonst bei neuen Einträgen.", store.IntentQuestion, []string{extID["a7"]}, 0, 3*time.Hour, "")
	send("a7", "", pw, "Cursor, 50 pro Seite, absteigend nach created_at. Offset habe ich im Admin schon einmal bereut.", "", nil, q, 2*time.Hour+50*time.Minute, "")
	send("a3", "", pw, "Die Migration 0042 setzt prospects.source auf NOT NULL. In Prod gibt es 214 Zeilen ohne Quelle. Backfill mit 'import' oder diese Zeilen verwerfen? Ohne Antwort lasse ich die Migration liegen und baue die Webhooks weiter.", store.IntentQuestion, []string{r1}, 0, 2*time.Hour+10*time.Minute,
		"Eine Datenbank-Änderung würde eine Pflichtspalte einführen. 214 alte Zeilen haben dort keinen Wert. Du entscheidest, ob sie 'import' bekommen oder gelöscht werden. Empfehlung: Backfill, löschen lässt sich nicht rückgängig machen.")
	send("a7", "", pw, "Export als CSV mit Semikolon und UTF-8-BOM, damit Excel bei den Kunden die Umlaute richtig liest? Ich baue es so, sobald du ja sagst.", store.IntentApproval, []string{r1}, 0, 95*time.Minute,
		"Der Vertrieb öffnet den Export in Excel. Mit Semikolon und BOM klappt das ohne Importdialog. Du gibst die Variante frei, bevor sie gebaut wird.")
	send("a8", "", pw, "Zweite Frage: darf der Endpunkt `status=any` annehmen oder nur die vier festen Werte? Die Tabelle sendet bisher beides.", store.IntentQuestion, []string{extID["a7"]}, 0, 25*time.Minute, "")
	send("robin", "", pw, "Seed-Daten bitte erst nach dem Merge von 0042 aktualisieren, sonst scheitert das Staging.", "", []string{extID["a4"]}, 0, 4*time.Hour, "")

	// ghosttree
	send("robin", "", gt, "Die Review-Karten sind zu dicht: Titel, Ablageort, Prüfhinweis und Erklärung stehen in einem Block. Bitte trennen und das Wichtigste nach vorn.", "", []string{extID["a1"]}, 0, 6*time.Hour, "")
	send("a1", "", gt, "Verstanden. Ich trenne in Kopf (Titel, Herkunft, Alter), Beleg (Pfad als Monospace-Zeile) und eine eingeklappte Erklärung. PR kommt heute.", "", nil, 0, 5*time.Hour+40*time.Minute, "")
	send("a2", "", gt, "Gärtner-Rotation ist gemergt (PR #128). Offen ist nur noch die Anzeige der Grenzen auf der Admin-Seite; das steht in REQ-457.", store.IntentHandoff, []string{r1}, 0, 3*time.Hour,
		"Die Arbeit an der Gärtner-Rotation ist abgeschlossen und übergeben. Es bleibt ein kleiner Punkt in der Admin-Anzeige. Nimm die Übergabe an, wenn du den Rest selbst machen willst.")

	// arkon
	send("a5", "", ar, "Der Import schneidet bei Zeile 50.000 ab, weil der Puffer ohne Fehler überläuft. Ich teile in Blöcke zu 5.000 und prüfe die Summen danach.", "", nil, 0, 2*time.Hour+30*time.Minute, "")
	send("a9", "", ar, "Welche Zeitzone gilt für Zeilen ohne Offset: Europe/Berlin oder UTC? Im Export von März sind beide Varianten gemischt, 1.340 Zeilen betroffen.", store.IntentQuestion, []string{"person:2"}, 0, 70*time.Minute,
		"Manche Zeilen der Importdatei haben keine Zeitzone. Je nach Annahme verschieben sich 1.340 Termine um eine bis zwei Stunden. Du entscheidest, was gelten soll. Empfehlung: Europe/Berlin, weil die Kunden dort sitzen.")
	send("philipp", "", ar, "Kurz zur Info: der Block-Import muss auch mit dem alten Format von 2024 laufen.", "", []string{extID["a5"]}, 0, 50*time.Minute, "")

	// design system
	send("a6", "", ds, "Dunkle Varianten für 14 Tokens fertig. Bei `ink-soft` auf `surface-2` komme ich nur auf Kontrast 3,8; ich hebe den Ton an, sofern das für dich passt.", store.IntentApproval, []string{r1}, 0, 40*time.Minute,
		"Ein Textton ist auf dem dunklen Hintergrund zu schwach (3,8 statt 4,5). Der Agent möchte ihn heller machen. Das ändert das Aussehen leicht, bleibt aber im Rahmen der Palette.")

	// infra
	send("a10", "", infr, "restic-Repository ist angelegt, der Schlüssel liegt noch nicht auf der Storage Box. Ohne ihn kann ich den ersten Lauf nicht testen.", store.IntentBlocker, []string{"person:2"}, 0, 7*time.Hour, "")
	send("a11", "", infr, "Übergabe: Prod-Rollout war grün, die Disk-Warnung auf apps-vm ist offen.", store.IntentHandoff, []string{"person:2"}, 0, 3*24*time.Hour, "")

	// ---- Aufgaben --------------------------------------------------------
	taskExpl := map[int64]string{}
	mkTask := func(acc *store.ProjectAccess, actor humantask.Actor, project string, in humantask.CreateInput, expl string, age time.Duration) humantask.Task {
		in.Project = project
		t, err := acc.CreateHumanTask(in, actor)
		must(err)
		if expl != "" {
			must(st.SaveAgentExplanation("task", t.ID, expl))
			taskExpl[t.ID] = expl
		}
		backdate = append(backdate, struct {
			id  int64
			age time.Duration
		}{-t.ID, age})
		return t
	}
	mkTask(robin, robinActor, pw, humantask.CreateInput{RequestID: reqID[3], Kind: "secret", Title: "Stripe-Live-Schlüssel für pixelworks im Vault ablegen",
		Description: "Für den Live-Betrieb braucht pixelworks einen eigenen Stripe-Schlüssel mit eingeschränkten Rechten (nur Checkout und Webhooks, keine Auszahlungen). Lege im Stripe-Dashboard unter Entwickler > API-Schlüssel einen eingeschränkten Schlüssel an, kopiere ihn und speichere ihn im Vault. Den Schlüssel bitte nicht in den Chat oder in eine Datei schreiben.",
		Location:    "Agent Vault: pixelworks/stripe/live (Feld secret_key)",
		CheckHint:   "Der Eintrag existiert; `agent-vault get pixelworks/stripe/live --field secret_key | head -c 8` zeigt `rk_live_`."},
		"Der Agent kann die Zahlungen erst live schalten, wenn der Schlüssel im Vault liegt. Du erzeugst ihn in Stripe und legst ihn dort ab; der Agent prüft danach nur, ob der Eintrag da ist. Nichts davon landet im Chat.", 26*time.Hour)
	mkTask(robin, robinActor, ar, humantask.CreateInput{RequestID: reqID[5], Kind: "file", Title: "Schema-Dump der Altdatenbank bereitlegen",
		Description: "Der Trockenlauf der Migration braucht einen Dump des alten Schemas v3 mit Daten, ohne Personenbezug (Spalten email und phone sind bereits maskiert). Erzeugen auf apps-vm mit: `pg_dump --schema=public --no-owner --no-privileges arkon_prod > /srv/arkon/import/2026-10/schema-dump.sql`. Danach die Datei auf diese Maschine kopieren.",
		Location:    "/srv/arkon/import/2026-10/schema-dump.sql",
		CheckHint:   "Die Datei existiert, ist größer als 40 MB und endet mit `COMMIT;`; `sha256sum` stimmt mit der Prüfsumme in der Datei schema-dump.sha256 überein."},
		"Der Agent will die Migration erst gegen eine Kopie der echten Struktur ausprobieren. Dafür fehlt ihm die Dump-Datei, die nur du von Prod ziehen kannst. Es geht um Struktur, personenbezogene Spalten sind schon unkenntlich.", 3*24*time.Hour)
	mkTask(robin, robinActor, pw, humantask.CreateInput{RequestID: reqID[3], Kind: "input", Title: "Preisstufen für das Pro-Paket festlegen", Assignee: "Philipp",
		Description: "Für die Checkout-Seite fehlen die endgültigen Preise. Zur Wahl stehen A) 29 EUR pro Monat mit 3 Plätzen, B) 49 EUR pro Monat mit 10 Plätzen, C) 39 EUR pro Monat mit 5 Plätzen und jährlicher Zahlung. Antwort bitte als Buchstabe mit einem Satz Begründung."},
		"Die Checkout-Seite kann ohne feste Preise nicht fertig werden. Drei Varianten liegen vor, du wählst eine. Das ist eine Geschäftsentscheidung, die der Agent nicht treffen soll.", 20*time.Hour)
	mkTask(robin, robinActor, infr, humantask.CreateInput{RequestID: reqID[7], Kind: "action", Title: "SSH-Schlüssel auf der Hetzner Storage Box hinterlegen",
		Description: "restic verbindet sich per SFTP mit der Storage Box. Dafür muss der öffentliche Schlüssel dort stehen. Ausführen auf robin-arch: `cat ~/.ssh/id_ed25519_backup.pub | ssh -p 23 u412233@u412233.your-storagebox.de install-ssh-key`. Das Passwort steht in der Hetzner-Konsole unter Storage Boxes.",
		Location:    "~/.ssh/id_ed25519_backup.pub",
		CheckHint:   "`sftp -P 23 -i ~/.ssh/id_ed25519_backup u412233@u412233.your-storagebox.de <<< ls` läuft ohne Passwortabfrage."},
		"Die Sicherungen können erst testen, wenn die Storage Box den Schlüssel dieser Maschine kennt. Das ist ein einmaliger Schritt mit deinem Hetzner-Passwort.", 7*time.Hour)
	mkTask(robin, robinActor, gt, humantask.CreateInput{RequestID: reqID[0], Kind: "action", Everyone: true, Title: "DNS-Eintrag für ghosttree-staging.deadweight.dev setzen",
		Description: "Für die Staging-Instanz fehlt der A-Record. In Cloudflare unter deadweight.dev > DNS einen A-Eintrag `ghosttree-staging` auf 157.90.12.34 anlegen, Proxy aus (grau). Wer von euch beiden zuerst Zeit hat.",
		CheckHint:   "`dig +short ghosttree-staging.deadweight.dev` liefert 157.90.12.34."},
		"Eine Staging-Adresse fehlt im DNS. Es ist egal, wer den Eintrag setzt, aber einer muss es tun. Danach kann der Agent die Review-Seite dort zeigen.", 2*24*time.Hour+3*time.Hour)
	mkTask(phil, philActor, pw, humantask.CreateInput{RequestID: reqID[2], Kind: "input", Title: "Kundenfreigabe für den CSV-Export einholen",
		Description: "Der Vertrieb von Kunde Hartmann will vor dem Livegang sehen, welche Spalten im Export stehen: Name, Firma, E-Mail, Telefon, Status, Quelle, Erstellt am. Bitte per Mail abstimmen und die Antwort hier in der Notiz festhalten, besonders ob die Telefonnummer enthalten sein darf (Datenschutz)."},
		"Ob die Telefonnummer in den Export darf, ist eine Frage an den Kunden. Der Agent baut die Spalten erst fertig, wenn die Antwort da ist.", 5*time.Hour)
	mkTask(robin, robinActor, ds, humantask.CreateInput{RequestID: reqID[6], Kind: "file", Title: "Logo als einfarbiges SVG ablegen",
		Description: "Für den Dunkelmodus brauchen wir das Logo einfarbig (currentColor). Aus dem Figma-Export `logo-wordmark` als SVG, Pfade zusammengefasst, ohne feste Farbe.",
		Location:    "packages/tokens/assets/logo-mono.svg",
		CheckHint:   "Die Datei existiert und enthält `fill=\"currentColor\"`, keine Hex-Farben."},
		"Der Dunkelmodus braucht ein Logo, das seine Farbe vom Umfeld nimmt. Die Datei kommt aus Figma und liegt nur bei dir.", 9*time.Hour)
	done := mkTask(robin, robinActor, pw, humantask.CreateInput{RequestID: reqID[3], Kind: "action", Title: "Webhook im Stripe-Dashboard aktivieren", Assignee: "Philipp",
		Description: "Endpunkt https://pixelworks.example/api/stripe/webhook, Ereignisse checkout.session.completed und customer.subscription.deleted."}, "", 30*time.Hour)
	_, err = phil.MarkTasksDone([]int64{done.ID}, "aktiviert, Endpunkt antwortet mit 200", philActor)
	must(err)

	// ---- Pains -----------------------------------------------------------
	painActor := pain.Actor{AccountID: 1, Person: "Robin"}
	mkPain := func(project, title, body string, motivates ...pain.Target) int64 {
		d, err := robin.CreatePain(store.CreatePainInput{Project: project, Title: title, Body: body, Motivates: motivates, Actor: painActor})
		must(err)
		return d.Pain.ID
	}
	mkPain(gt, "Wissensvorschläge stauen sich, keiner fühlt sich zuständig",
		"26 Vorschläge stehen offen, vier davon seit zwei Wochen. Niemand sieht beim Öffnen der Seite, welche wichtig sind; alte und neue sehen gleich aus. Wer sie nicht prüft, lässt Agenten mit veraltetem Wissen arbeiten.",
		pain.Target{RequestID: reqID[0]})
	mkPain(gt, "Agenten verlieren nach der Kontextkomprimierung den Faden",
		"Nach einer Komprimierung kennt der Agent zwar den Auftrag, aber nicht mehr, welche Datei er gerade ändern wollte und warum. Er beginnt die Suche von vorn und wiederholt Schritte. Beobachtet in sieben der letzten zehn langen Sitzungen.")
	m := mkPain(pw, "Migrationen laufen auf Prod ohne Trockenlauf",
		"Die Migration 0039 hat in Prod 12 Minuten Sperre auf prospects verursacht. Ein Trockenlauf gegen eine Kopie hätte das gezeigt.")
	_, err = robin.SetPainState(m, "mitigated", "Trockenlauf existiert für arkon, nicht für pixelworks.", pain.Evidence{Kind: "commit", Ref: "5d2a7c9"}, "", painActor)
	must(err)
	mkPain(ar, "Import hat keinen Fortschritt, nur Erfolg oder Abbruch",
		"Der nächtliche Import läuft bis zu 40 Minuten und schreibt erst am Ende eine Zeile ins Log. Bei einem Abbruch weiß niemand, wie weit er kam.", pain.Target{RequestID: reqID[4]})
	r := mkPain(ds, "Dunkelmodus-Tokens driften zwischen Design und Code", "Figma und tokens.css haben unterschiedliche dunkle Grautöne; Abweichungen fallen erst im Browser auf.")
	_, err = robin.SetPainState(r, "resolved", "", pain.Evidence{Kind: "commit", Ref: "9c1e4f2"}, "", painActor)
	must(err)

	// ---- Wissen ----------------------------------------------------------
	type kn struct {
		project, typ, title, body, conf, origin, harness, person string
		age                                                      time.Duration
		expl                                                     string
	}
	prop := func(project, typ, title, body, person string, age time.Duration, expl string) kn {
		return kn{project, typ, title, body, "staged", "agent", "claude", person, age, expl}
	}
	dist := func(project, typ, title, body string, age time.Duration) kn {
		return kn{project, typ, title, body, "quarantined", "distilled", "claude", "Robin", age, ""}
	}
	garden := func(project string, id int, label, subject, reason string, age time.Duration) kn {
		return kn{project, "note", fmt.Sprintf("[Gärtner] %s #%d: %s", label, id, subject),
			fmt.Sprintf("Vorschlag des Gärtners (Herkunft gardener, nicht bestätigt) zu Eintrag #%d: %s\n\nUrteil: %s\n\nBegründung: %s\n", id, subject, label, reason),
			"staged", "distilled", "gardener", "gardener", age, "Der Gärtner hat diesen Eintrag geprüft und hält ihn für überholt. Du bestätigst, dass er überholt ist, oder verwirfst den Vorschlag. Der ursprüngliche Eintrag bleibt in beiden Fällen unverändert, bis du entscheidest."}
	}
	d := 24 * time.Hour
	items := []kn{
		prop(gt, "pitfall", "html/template escaped Attribute in href nicht doppelt", "Wer in einem href `{{.URL}}` setzt, bekommt eine gekürzte URL, wenn der Wert `&` enthält. Mit `urlquery` nur für Query-Teile arbeiten, nie für die ganze URL.", "Robin", 14*d, "Eine Falle aus der Template-Engine: URLs mit & werden verändert. Wenn du es bestätigst, lesen künftige Agenten die Warnung."),
		prop(gt, "decision", "Review bündelt Vorschläge nach Projekt", "Entschieden am 2026-10-16: Die Review-Seite zeigt Vorschläge je Projekt gebündelt, die Sammelaktion gilt nur innerhalb einer Gruppe. Verworfen: eine globale Sammelaktion, weil sie Vorschläge aus Projekten trifft, die man nicht angesehen hat.", "Robin", 6*time.Hour, "Eine Entscheidung zur Gestaltung der Review-Seite mit Begründung, damit niemand sie später aus Versehen umkehrt."),
		prop(gt, "pitfall", "ctx doc push scheitert still bei fehlendem Titel", "Ein Dokument ohne Titel im Kopf wird nicht abgelehnt, sondern ohne Eintrag im Verzeichnis abgelegt. Immer zuerst `ctx doc show` prüfen.", "Philipp", 14*d, ""),
		prop(gt, "note", "Playwright headless: Schrift lädt erst nach 300 ms", "Screenshots direkt nach `goto` zeigen die Ersatzschrift. `waitForTimeout(300)` oder auf `document.fonts.ready` warten.", "Robin", 9*d, ""),
		prop(gt, "pitfall", "Sqlite im WAL-Modus: zweiter Prozess sperrt den Server", "Ein zweiter Schreiber auf derselben Datei blockiert den Writer des Servers bis zum Timeout. Für Seeds immer den Server vorher stoppen.", "Philipp", 3*d, "Warnt davor, die Datenbank von außen zu beschreiben, solange der Server läuft."),
		prop(gt, "decision", "Zwei Sprachen: Katalogtest erzwingt beide Dateien", "Jeder neue Text muss in messages.go und messages_de.go stehen. Ein Test vergleicht die Schlüssel.", "Robin", 2*d, ""),
		prop(pw, "pitfall", "Stripe-Webhook: Rohbody vor dem Parsen prüfen", "Die Signatur gilt für den unveränderten Body. Wer ihn erst als JSON liest und wieder serialisiert, bekommt `signature mismatch`. Den Body als Bytes lesen, prüfen, dann parsen.", "Robin", 4*d, "Eine typische Falle bei Stripe-Webhooks, die schon einmal Zeit gekostet hat."),
		prop(pw, "decision", "Prospects: Cursor statt Offset", "Die Admin-Liste pagt mit Cursor (created_at, id). Offset springt bei neuen Einträgen und wird bei großen Tabellen langsam.", "Philipp", 3*time.Hour, "Hält fest, warum die Liste mit Cursor arbeitet."),
		prop(pw, "note", "Staging-Datenbank wird nachts um 03:00 zurückgesetzt", "Alles, was in Staging von Hand angelegt wird, ist am nächsten Morgen weg. Testdaten gehören ins Seed-Skript.", "Philipp", 11*d, ""),
		prop(pw, "pitfall", "CSV-Export: Excel braucht BOM für Umlaute", "Ohne UTF-8-BOM zeigt Excel Umlaute falsch an. Semikolon als Trenner, weil Kommas in deutschen Zahlen stehen.", "Philipp", 90*time.Minute, ""),
		prop(pw, "pitfall", "Migration 0039 sperrt prospects für 12 Minuten", "ALTER TABLE mit NOT NULL ohne Default sperrt die Tabelle. Besser: Spalte nullable anlegen, backfillen, dann Constraint setzen.", "Robin", 5*d, "Ein Fehler aus der Praxis, der sich bei der nächsten Migration wiederholen würde."),
		prop(ar, "pitfall", "Import-Puffer läuft ohne Fehler über", "Bei über 50.000 Zeilen schneidet der Reader still ab. Blockweise lesen und am Ende die Summe gegen die Zeilenzahl der Datei prüfen.", "Robin", 2*time.Hour, ""),
		prop(ar, "note", "Zeiten im Export von März sind gemischt", "Ein Teil der Zeilen hat Offset, ein Teil nicht. Annahme für Zeilen ohne Offset muss entschieden werden (offene Frage an Philipp).", "Philipp", 70*time.Minute, ""),
		prop(ar, "decision", "Schema v3 wird nicht in place migriert", "Die Migration baut einen neuen Store und kopiert; das alte Schema bleibt 14 Tage lesbar.", "Robin", 8*d, ""),
		prop(ds, "pitfall", "ink-soft auf surface-2 verfehlt AA im Dunkelmodus", "Kontrast 3,8:1 statt 4,5:1. Ton von #8a97a8 auf #a3afbf anheben.", "Robin", 40*time.Minute, ""),
		prop(ds, "note", "Tokens werden aus tokens.css gebaut, nicht aus Figma", "Figma ist Vorlage, nicht Quelle. Änderungen gehen in tokens.css und dann zurück nach Figma.", "Robin", 12*d, ""),
		prop(infr, "pitfall", "restic: Passwortdatei nicht im Repository-Ordner ablegen", "Wer die Passwortdatei neben dem Repository liegen lässt, macht die Verschlüsselung wirkungslos.", "Philipp", 6*time.Hour, "Eine Sicherheitsfalle bei der neuen Sicherung."),
		prop(infr, "decision", "Backups: 14 Tage Aufbewahrung, danach prune", "Täglich 14, wöchentlich 8. Mehr kostet auf der Storage Box nur Platz, aber keinen Nutzen.", "Philipp", 7*time.Hour, ""),
		dist(gt, "note", "Review-Aktionen laufen über POST /ui/review/{id}", "Aus Sitzung abgeleitet: Bestätigen und Verwerfen sind zwei Formulare mit CSRF-Token.", 4*d),
		dist(pw, "note", "Prospects haben vier Status: neu, kontaktiert, Angebot, verloren", "Aus Sitzung abgeleitet, noch nicht bestätigt.", 5*d),
		dist(ar, "pitfall", "Die Importdatei von 2024 hat Semikolon als Trenner", "Aus Sitzung abgeleitet. Neuere Dateien nutzen Komma.", 6*d),
		garden(gt, 1840, "Veraltet?", "Hook läuft über Node-Skript", "Der Hook ist seit PR #96 ein Go-Programm; Node wird nirgends mehr aufgerufen.", 20*time.Hour),
		garden(gt, 2113, "Korrektur?", "Sessions werden nach 30 Tagen gelöscht", "Die Aufbewahrung ist konfigurierbar, Standard 90 Tage (cmd/ctx/serve.go).", 20*time.Hour),
		garden(pw, 2290, "Veraltet?", "Staging läuft auf Heroku", "Staging wurde im September nach Hetzner umgezogen.", 20*time.Hour),
		garden(pw, 2301, "Korrektur?", "Stripe API-Version 2022-11-15", "Das Projekt nutzt seit August 2024-06-20.", 20*time.Hour),
		garden(ar, 2415, "Veraltet?", "Import nutzt Python-Skript import.py", "Das Skript wurde durch den Go-Job ersetzt (internal/importer).", 20*time.Hour),
		garden(ds, 2502, "Veraltet?", "Farben stehen in colors.scss", "Seit dem Umbau liegen sie in tokens.css; colors.scss existiert nicht mehr.", 20*time.Hour),
	}
	var kids []int64
	for _, k := range items {
		person := k.person
		id, err := st.InsertKnowledge(store.Knowledge{Type: k.typ, Title: k.title, Body: k.body, Scope: scope.Axes{Project: k.project}, Person: person, Confidence: k.conf, Origin: k.origin, Harness: k.harness, SessionRef: map[bool]string{true: "gardener:check-knowledge:1", false: ""}[k.harness == "gardener"]})
		must(err)
		kids = append(kids, id)
		if k.expl != "" {
			must(st.SaveAgentExplanation("knowledge", id, k.expl))
		}
		backdate = append(backdate, struct {
			id  int64
			age time.Duration
		}{-1_000_000 - id, k.age})
	}
	// Bestehendes Wissen (nicht in Review), damit die Wissensseite nicht leer wirkt.
	for i, t := range [][2]string{
		{"Deploy läuft über den Tag-Push", "Ein Tag v* startet die Release-Pipeline; Prod wird von Hand ausgerollt."},
		{"Server startet mit GHOSTTREE_LANGUAGE=de", "Prod läuft deutsch; neue Texte brauchen beide Kataloge."},
		{"Branch-Schutz auf dev", "Ohne grüne Prüfungen kein Merge, auch nicht für Admins."},
		{"CLA-Haken im PR-Body", "Der Policy-Check liest den Body des ursprünglichen Ereignisses."},
		{"Agent Vault für Schlüssel", "Geheimnisse nie im Chat, immer im Vault ablegen und nur den Ort nennen."},
	} {
		_, err := st.InsertKnowledge(store.Knowledge{Type: []string{"note", "note", "decision", "pitfall", "decision"}[i], Title: t[0], Body: t[1], Scope: scope.Axes{Project: []string{gt, gt, gt, gt, infr}[i]}, Person: "Robin", Confidence: "verified", ConfirmedBy: "Robin"})
		must(err)
	}
	_ = kids

	out["robin_project"] = gt
	// Ein Anmeldecode gilt einmal; die Fotoskripte brauchen mehrere.
	for _, n := range []string{"Robin", "Philipp"} {
		for i := 0; i < 4; i++ {
			c, _, err := st.CreateAccountCode(store.CodeLogin, n)
			must(err)
			key := map[string]string{"Robin": "robin", "Philipp": "philipp"}[n]
			if i > 0 {
				key += strconv.Itoa(i + 1)
			}
			out[key] = c
		}
	}
	tok, _, err := st.CreateDeviceToken("person:1", "demo")
	must(err)
	out["robin_token"] = tok

	// Zeitstempel zurückdatieren, solange der Store offen ist, geht nicht an dessen Verbindung vorbei:
	// ein eigener Zugriff nach dem Schließen.
	must(st.Close())
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	must(err)
	defer db.Close()
	for _, b := range backdate {
		ts := ago(b.age)
		switch {
		case b.id > 0:
			_, err = db.Exec(`UPDATE coord_messages SET created_at=? WHERE id=?`, ts, b.id)
			must(err)
			_, err = db.Exec(`UPDATE coord_attention SET created_at=? WHERE message_id=?`, ts, b.id)
			must(err)
		case b.id <= -1_000_000:
			_, err = db.Exec(`UPDATE knowledge SET created_at=?, updated_at=?, observed_at=? WHERE id=?`, ts, ts, ts, -b.id-1_000_000)
			must(err)
		default:
			_, err = db.Exec(`UPDATE human_tasks SET created_at=?, updated_at=? WHERE id=?`, ts, ts, -b.id)
			must(err)
		}
	}
	titles := map[string]string{}
	for i, a := range agents {
		titles["s-"+uuid(i+1)] = a.title
	}
	for ext, t := range titles {
		_, err = db.Exec(`UPDATE sessions SET title=? WHERE external_id=?`, t, ext)
		must(err)
	}
	fmt.Fprintln(os.Stderr, "seeded rebuild")
	return out
}
