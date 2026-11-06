# Agent-Memory-Vergleich: Wissen über Sessions hinweg

Dieser Harness misst, ob ein Coding-Agent Projektwissen aus früheren Sessions
in einer frischen Session richtig anwendet. Er vergleicht vier Arme:

| Arm | Gedächtnis |
|---|---|
| `vanilla` | keins (Auto-Memory aus, selbst angelegte Gedächtnisdateien werden nach jeder Session entfernt) |
| `native` | Claude Codes eingebaute Auto-Memory, `CLAUDE.md` erlaubt |
| `claudemem` | claude-mem, offizieller Installweg `npx claude-mem install --provider claude` |
| `ghosttree` | `ctx setup` + `ctx install claude` gegen einen frischen lokalen `ctx serve` je Kette |

Präregistrierung und Ergebnis stehen im ghosttree-Dokument
`agent-memory-vergleich-2026-11` (Revision 1 = Präregistrierung). Die erste
Messung lief am 2026-11-05.

## Ergebnis der ersten Messung (2026-11-05/06)

Claude Opus 5.5, Claude Code 2.1.318. Erfolg heißt Aufgabe erfüllt und
Projektwissen aus einer früheren Session eingehalten.

| Projekt | vanilla | native | claude-mem 15.10.0 | ghosttree |
|---|---|---|---|---|
| ledgerly, 48 Folgesessions je Arm | 11/48 | 48/48 | 12/48 | 48/48 |
| NurLearn, 12 Folgesessions je Arm | 0/12 | 11/12 (12/12 neu bewertet) | 0/12 | 12/12 |

ghosttree liegt gleichauf mit Claude Codes Auto-Memory. Auf ledgerly kostet
es rund 42 % mehr pro Session, auf NurLearn gleich viel.

Die Spalten vanilla und claude-mem taugen nicht als Vergleich. Ein
unabhängiges Audit (Dokument `audit-agent-memory-vergleich-2026-11`) fand:

- claude-mem: Im Kopflos-Modus (`claude -p`) beendet sich Claude Code, bevor
  der asynchrone Stop-Hook von claude-mem läuft. Dadurch entstand in keiner
  Kette eine Session-Zusammenfassung, die claude-mem sonst beim Start
  mitliefert. Mit nachgeholtem Stop-Hook bestand claude-mem eine
  ungezählte Stichprobe vollständig. Der Harness gleicht das für ghosttree
  (`ctx watch --once`) aus, für claude-mem nicht.
- vanilla: Der Arm schrieb in den Lern-Sessions eine `CLAUDE.md`, die der
  Harness vor der Folge-Session löscht. Das ist ein künstlicher Boden, kein
  realistischer Nutzer ohne Gedächtnis.

Belegt ist nur: ghosttree hielt Projektregeln in 60 von 60 Folge-Sessions ein,
genauso wie die Auto-Memory. Wer den Harness erneut fährt, sollte beides
vorher beheben. Zusammenfassungen liegen in `results/`, Begründung,
Intervalle und Grenzen im Dokument `agent-memory-vergleich-2026-11`
(Revision 2).

## Ablauf einer Kette

Eine Kette ist ein Arm × eine Wiederholung. Jede Kette hat ein eigenes HOME
unter `$BENCH_RUNS`, ein eigenes Projekt und einen eigenen
Gedächtniszustand. Beim ghosttree-Arm kommen ein eigener Server, eine
eigene Datenbank und ein eigener Port dazu.

1. **Lernen:** L1–L3 nacheinander. Jede Session hat einen Arbeitsauftrag
   und danach (per `--resume`) eine Nutzeräußerung mit Projektwissen.
   Diese ist ausdrücklich unabhängig vom Auftrag und endet mit „merk sie dir
   bitte für künftige Sessions“ (`harness/prompts.json`).
2. **Schnappschuss** von HOME (inkl. Projekt und Gedächtnis), bei ghosttree
   zusätzlich der Server-Datenbank.
3. **Folgeaufgaben** F0–F6, jede als frische Session auf dem
   wiederhergestellten Schnappschuss. Die Prompts enthalten keinen Hinweis
   auf das Wissen.
4. **Bewertung** ohne Modell: Ein verdeckter Go-Test je Aufgabe
   (`hidden/`) wird in eine Kopie des Projekts gelegt. `func` heißt
   Aufgabe erfüllt und Repo-Tests grün, `know` heißt Projektwissen
   eingehalten, Erfolg ist beides.

`projects/ledgerly/` ist das Testprojekt, ein kleiner Go-Abrechnungsdienst
nur mit Standardbibliothek. Die zweite Messung lief auf einer Kopie eines
privaten Projekts (NurLearn). Dessen Vorlage, Aufgaben und verdeckte Tests
liegen deshalb nicht im Repo.
Mit `BENCH_NURLEARN_TEMPLATE` lässt sich eine eigene Kopie angeben.

## Voraussetzungen

- Linux, Go, Node/npx, `uv`, Python 3.11+, git
- Claude Code (`claude`) und ein Zugang, den `BENCH_AGENT` setzt: entweder
  `claude` selbst oder ein Wrapper, der `ANTHROPIC_BASE_URL` und
  `ANTHROPIC_AUTH_TOKEN` exportiert und `claude` aufruft
- `ctx` aus diesem Repo (`make build`)

## Sicherheit: Agenten laufen ohne Rückfragen

`bench.py` startet jede Session mit `--dangerously-skip-permissions`. Nur
so laufen Sessions unbeaufsichtigt, und alle Arme arbeiten mit demselben
Werkzeugumfang. Eigenes HOME, `env -i` und eigenes `TMPDIR` je Kette
trennen **nur die Konfiguration**. Eine Sicherheitsgrenze sind sie nicht:
Der Agent läuft unter der Benutzerkennung, die den Harness startet, und
kann per Bash alles lesen und ändern, was dieser Benutzer darf
(echtes HOME, SSH-Schlüssel, andere Repositories), dazu das Netz.

Deshalb nur auf einer Maschine oder unter einem Konto fahren, das dafür
vorgesehen ist:

- eigener, unprivilegierter Benutzer ohne Zugriff auf persönliche Daten
  und Schlüssel, `BENCH_BASE` und `BENCH_RUNS` gehören diesem Benutzer, oder
- jede Kette in einem Wegwerf-Container bzw. einer VM mit eingeschränktem
  Netz (nur der Modell-Endpunkt und die Paketquellen, die die Arme zum
  Installieren brauchen).

Eine Werkzeug-Allowlist (`--allowedTools`) statt
`--dangerously-skip-permissions` ist möglich. Sie ändert aber, was die
Agenten tun können, und ist dann ein anderes Messprotokoll als in der
Messung vom 2026-11-05/06.

## Einrichten

```bash
export BENCH_BASE=~/agentbench-data/agent-compare   # Ergebnisse, gemeinsame Binaries
export BENCH_RUNS=/var/tmp/agentcmp/runs            # Arbeitsbäume, NICHT unter $HOME
export BENCH_AGENT=claude                           # oder Pfad zu einem Wrapper
# nur für claude-mem hinter einem Proxy (der Observer ignoriert geerbte ANTHROPIC_*):
export BENCH_CMEM_GATEWAY_URL=http://proxy:port BENCH_CMEM_GATEWAY_TOKEN=...

S=$BENCH_BASE/shared; mkdir -p $S/bin $S/ctx-bin $S/gocache $S/gomod $S/npm-cache $S/uv-cache
ln -sf "$(readlink -f "$(command -v claude)")" $S/bin/claude    # Version pinnen
ln -sf "$(command -v uv)" $S/bin/uv
printf '#!/bin/sh\nexec uv tool run "$@"\n' > $S/bin/uvx && chmod +x $S/bin/uvx
printf '#!/bin/sh\necho "$(date -Is) systemctl $*" >> "$HOME/.bench-systemctl.log"\necho "Failed to connect to bus: No medium found" >&2\nexit 1\n' > $S/bin/systemctl && chmod +x $S/bin/systemctl
cp dist/ctx $S/ctx-bin/ctx
curl -fsSL https://bun.sh/install | BUN_INSTALL=$S/bun bash && ln -sf $S/bun/bin/bun $S/bin/bun
```

Die `systemctl`-Attrappe ist Absicht. `ctx install claude` würde sonst per
`systemctl --user` die Collector-Unit des echten Benutzers anfassen. Der
Collector läuft im Harness stattdessen als `ctx watch --once` nach jeder
Session.

Vor einer Messung die verdeckten Checks prüfen: Die Referenzlösung muss
bestehen, die naive Lösung muss `know` verfehlen.

```bash
python3 hidden/validate_ledgerly.py
```

## Messen

```bash
cd harness
python3 bench.py chain ghosttree 0                 # eine Pilotkette (Wiederholung 0 wird nicht gezählt)
python3 run_campaign.py --ledgerly-reps 8 --nurlearn-reps 0 --max-par 12
python3 analyze.py                                 # Tabelle, Kontraste, Mechanismus
python3 analyze.py --project nurlearn
python3 bench.py regrade <kette>                   # nur bei Harness-/Umgebungsfehlern
python3 killall.py                                 # alles stoppen
```

Je Kette liegen unter `$BENCH_BASE/results/<kette>/`: `records.jsonl`
(eine Zeile je Session mit Kosten, Tokens, Zügen, Bewertung,
Mechanismus, geladenen Instruktionsdateien), die Transkripte, die stdout-JSONs
und je Folgesession der Patch gegen den Schnappschuss.

## Fallen, die der Harness abfängt

- **CLAUDE.md aus Elternverzeichnissen:** Claude Code lädt `CLAUDE.md` und
  `.claude/CLAUDE.md` aller Vorfahren des Arbeitsverzeichnisses. Liegt
  `BENCH_RUNS` unter dem echten HOME, landet die persönliche
  `~/.claude/CLAUDE.md` in jedem Arm. Der Harness protokolliert je Session
  die geladenen Dateien (`instruction_files`) und markiert alles Fremde als
  `isolation_violations`.
- **Selbst angelegtes Gedächtnis:** Auf „merk dir das“ legt der Agent auch
  ohne Auto-Memory selbst `CLAUDE.md` oder Dateien im Auto-Memory-Pfad an.
  In allen Armen außer `native` entfernt der Harness das nach jeder Session
  und protokolliert es.
- **claude-mem-Observer:** Er braucht hinter einem Proxy die
  Gateway-Anmeldung in `~/.claude-mem/.env`, einen eigenen Worker-Port je
  Instanz und `uvx` für Chroma.
- **Unix-Sockets:** Das `TMPDIR` je Kette ist kurz (`/tmp/ac-<kette>`),
  weil Tests Sockets darunter binden (`sun_path` ≤ 108 Byte).
- **Modellidentität:** Ein Aufruf zählt nur, wenn das Hauptmodell in
  `modelUsage` ein `canonicalModel` mit Präfix `claude-opus-5-5` hat.
  Helfer sind nur Haiku 4.5/5.5 und Sonnet 5.5.
