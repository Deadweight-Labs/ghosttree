# Agent-Memory-Vergleich: Wissen über Sessions hinweg

Dieser Harness misst, ob ein Coding-Agent Projektwissen aus früheren Sessions
in einer frischen Session richtig anwendet. Er vergleicht vier Arme:

| Arm | Gedächtnis |
|---|---|
| `vanilla` | keins (Auto-Memory aus, selbst angelegte Gedächtnisdateien werden nach jeder Session entfernt) |
| `native` | Claude Codes eingebaute Auto-Memory, `CLAUDE.md` erlaubt |
| `claudemem` | claude-mem, offizieller Installweg `npx claude-mem install --provider claude` (nur noch in den Ergebnissen der ersten Messung; läuft nicht in der Sandbox) |
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

- Linux, Go, Python 3.11+, git, `socat`
- Docker mit dem Sandbox-Image (Standard) oder bubblewrap (`bwrap`, nur für lokale Probeläufe)
- Claude Code (`claude`): im Image bzw. bei bwrap der `claude` des Hosts
- ein Modellzugang: die Umgebungsvariable `ANTHROPIC_API_KEY` (oder die in
  `BENCH_UPSTREAM_KEY_ENV` genannte) gehört dem Modell-Proxy des Harness und nur ihm
- `ctx` aus diesem Repo (`make build`) für den ghosttree-Arm

## Sicherheit: Agenten laufen in einer Sandbox

`bench.py` startet jede Session mit `--dangerously-skip-permissions`. Nur so laufen Sessions
unbeaufsichtigt, und alle Arme arbeiten mit demselben Werkzeugumfang. Die Grenze zieht deshalb
die Sandbox (`harness/sandbox.py`), nicht der Agent. **Ohne nachgewiesene Isolation startet kein Agent.**

Jede Session, jeder `git`-, `ctx`- und `go test`-Aufruf auf Dateien aus dem Agenten-HOME läuft in einer
Sandbox mit:

- **keinem Netz** (Container: `--network none`; bwrap: `--unshare-all`). Der Agent erreicht weder das
  Internet noch den Host.
- **nur lesbarer Wurzel**, ohne Capabilities, mit `no-new-privileges`, als Benutzer ohne root, mit Grenzen
  für CPU, Speicher und Prozesse (nur Container).
- **Wegwerf-HOME** je Kette unter `$BENCH_RUNS`, eingehängt als `/home/agent`. Das HOME des Hosts
  und alles darunter ist nicht sichtbar. Gedächtnis eines Arms liegt nur in diesem HOME.
- **einer leeren Umgebung**: nur Namen aus `sandbox.ALLOWED_ENV_NAMES`, nichts wird geerbt. Auch bwrap
  selbst startet mit leerer Umgebung, weil sein Init-Prozess in der Sandbox bliebe und `/proc/1/environ`
  sonst die Umgebung des Harness verriete (die Probe hat das gefunden).
- **Modellzugang nur über einen Proxy** (`harness/modelproxy.py`): ein Unix-Socket je Kette, in die Sandbox
  eingehängt; `socat` macht daraus `127.0.0.1:18317`. Der Agent trägt als `ANTHROPIC_AUTH_TOKEN` ein
  Laufkennzeichen ein, keinen Schlüssel. Der Proxy lässt nur `POST /v1/messages` und
  `/v1/messages/count_tokens` mit gültigem Kennzeichen und einem Modell aus der Erlaubnisliste durch
  (`BENCH_PROXY_MODELS`, ein regulärer Ausdruck, der den ganzen Namen treffen muss; Standard erlaubt nur
  die Modelle der Messung mit optionalem Datum und `[1m]`). Er leitet nie die Rohbytes des Agenten weiter,
  sondern die geprüfte Anfrage, neu serialisiert; doppelte JSON-Schlüssel und Kopfzeilen mit Zeilenumbruch
  lehnt er ab. Er begrenzt Anfragen, Tokens, Gleichzeitigkeit und Verbindungen (60 s Lesefrist je
  Verbindung, höchstens 64 gleichzeitig) und schreibt den Verbrauch je Anfrage nach `proxy-usage.jsonl`.
  Ein Upstream über `http://` ist nur zu Loopback erlaubt. Den echten Schlüssel liest er erst bei einer Anfrage aus seiner
  eigenen Umgebung; er steht nie in der Umgebung der Sandbox, in einer Datei oder im Protokoll.
- **Bewertung ohne Modell**: Verdeckte Tests und Agentencode treffen sich nur in einer Bewertungs-Sandbox
  ohne Proxy, mit leerem HOME und dem Stand nur lesbar eingehängt. Der Harness folgt auf dem Host keinem
  Symlink aus der Sandbox und liest nur reguläre Dateien.

**Isolationsprotokoll.** Vor und nach jeder Session führt der Harness die Probe `harness/probe.py` in
derselben Sandbox aus, als derselbe Benutzer mit derselben Umgebung, und schreibt
`results/<kette>/<tag>.isolation.json` (Hash in den Metadaten der Session). Die Probe prüft: keine
Verbindung zu öffentlichen Adressen (und zu den Zielen aus `BENCH_PROBE_EXTRA_TARGETS`, einer Liste
`host:port,host:port` aus der Umgebung, damit keine internen Adressen im Repo stehen), keine
Namensauflösung, nur die Schnittstelle `lo`, das HOME des Hosts und der Docker-Socket fehlen, die
Umgebung trägt genau die erlaubten Namen und kein Prozess einen Schlüsselnamen, die Einhängungen sind genau
die erwarteten, die Wurzel ist nicht beschreibbar, keine Capabilities, der Proxy antwortet und weist
ein falsches Kennzeichen, ein fremdes Modell und einen fremden Pfad ab. Die Wurzel ist nicht beschreibbar,
keine Capabilities, und der Kernel filtert Systemaufrufe (`Seccomp` in `/proc/self/status` ist nicht 0).
Die Probe läuft mit absolutem Interpreter (`/usr/bin/python3 -I -S`) und einem PATH ohne HOME, damit ein
`python3` im beschreibbaren `~/.local/bin` des Agenten sie nicht fälschen kann. Die Nachher-Probe läuft in
einer **frischen Instanz** (neuer Container, neues bwrap, dieselben Einhängungen), nachdem alle Prozesse der
Sitzung beendet sind: ein mit `setsid` gestarteter Hintergrundprozess kann sie nicht beantworten. Dazu prüft der Harness vor der Probe die
Beschreibung der Sandbox (`verify_spec`) und bei Docker den gestarteten Container (`docker inspect`).
`verify_spec` verlangt für jede Einhängung `realpath == abspath` (ein Symlink aus der Sandbox ist nie eine
Quelle), und der Harness prüft die Verzeichnisse im HOME des Agenten unmittelbar vor jedem Einhängen
(`require_real_dirs`). Jeder `git`-Aufruf auf einem Repo des Agenten läuft mit `core.fsmonitor=false` und
`core.hooksPath=/dev/null`. `BENCH_BASE`, `BENCH_RUNS` und `BENCH_HIDDEN` werden beim Start aufgelöst und
müssen selbst ohne Symlink dastehen.
**Geschlossen bei Fehler:** Besteht eine dieser Stufen nicht, oder fehlt eine Prüfung in der Ausgabe der
Probe, wirft der Harness `IsolationError`, bevor der Agent läuft, und die Kette bricht ab. Es gibt keinen Weg
„ohne Sandbox“ (`BENCH_SANDBOX=none` ist ein Fehler).

**Zwei Wege hinter einer Schnittstelle** (`BENCH_SANDBOX`):

| Weg | Wert | Stärken | Grenzen |
|---|---|---|---|
| Container (Standard) | `container` | festes Image mit Toolchain, Grenzen für Speicher, Prozesse und CPU, läuft auf anderen Hosts | braucht den Docker-Daemon, Image bauen und pflegen |
| bubblewrap | `bwrap` | kein Daemon, kein root, Start in Millisekunden | Werkzeuge vom Host (`/usr`), keine Speicher- und Prozessgrenzen, der Weg steht im Protokoll |

bwrap startet mit `--unshare-all`, `--disable-userns` (wenn die installierte Version es kennt; das Protokoll
hält fest, ob) und einem Seccomp-Filter (`sandbox.seccomp_filter`: neue Namensräume, Einhängen, Module, `bpf`,
`io_uring`, `perf`, Schlüsselbund, Uhr und Neustart sind gesperrt; nur x86_64 und aarch64, sonst startet bwrap
nicht). Das ist weniger als ein Container: der Kernel des Hosts und seine Werkzeuge bleiben in Reichweite,
und es gibt keine Grenzen für Speicher und Prozesse. **Bubblewrap ist für Probeläufe gedacht; gewertete Läufe
laufen im Container.** Welcher Weg für gewertete Läufe gilt, ist eine offene Entscheidung (DEC-18, Empfehlung: Container).

**Der ghosttree-Arm erreicht einen Server auf dem Host.** Der Agent spricht über `gt.sock` (ein Unix-Socket,
von `socat` auf dem Host an den `ctx serve` der Kette gehängt) mit einem ghosttree-Server, der **nicht in der
Sandbox** läuft, sondern als Prozess des Harness-Benutzers auf `127.0.0.1`. Der Agent trägt dafür das Token der
Person `dev` im HOME (`ctx setup`) und hat damit alle Rechte dieser Person auf diesem Server: Wissen, Aufträge und
Ideen lesen und schreiben. Der Server hat eine eigene, frische Datenbank unter `$BENCH_RUNS/<kette>/gt` und ein
eigenes HOME ohne Zugangsdaten, `socat` und der Server starten mit knapper Umgebung. Die Angriffsfläche ist die
HTTP-Schnittstelle dieses `ctx`-Binaries (ein Fehler dort wäre ein Fehler auf dem Host); die anderen Arme haben
diesen Weg nicht.

**Grenzen dieser Sandbox.** Was ein Agent kosten kann, begrenzen die Modell-Erlaubnisliste und die Grenzen des
Proxys je Kette: `BENCH_PROXY_MAX_REQUESTS` (Standard 6000) und `BENCH_PROXY_MAX_TOKENS` (Standard 600 Millionen,
gezählt werden Eingabe, Ausgabe und beide Cache-Felder; erst nach jeder Antwort, es können also bis zu vier Anfragen
darüber hinaus laufen). Ist eine Grenze erreicht, antwortet der Proxy mit 429 ohne Wiederholung. Die Standardwerte
fangen einen entlaufenen Agenten ab, sie sind kein Budget; wer enger planen will, setzt beide Variablen. Der Arm `claudemem` läuft nicht
in der Sandbox (Installation und Worker brauchen Netz) und wird abgelehnt. Go-Module ohne Vendor-Verzeichnis
brauchen einen gefüllten Modul-Cache unter `$BENCH_BASE/shared/gomod` (nur lesbar eingehängt, `GOPROXY=off`);
ledgerly nutzt nur die Standardbibliothek.

## Einrichten

```bash
export BENCH_BASE=~/agentbench-data/agent-compare   # Ergebnisse, gemeinsame Binaries
export BENCH_RUNS=/var/tmp/agentcmp/runs            # Arbeitsbäume
export BENCH_SANDBOX=container                      # oder bwrap
export BENCH_IMAGE=agent-compare:local              # Image des Containers
# Schlüssel des Modells: nur für den Proxy des Harness, nie für Agenten
export ANTHROPIC_API_KEY=...                        # oder BENCH_UPSTREAM_KEY_ENV=<Name der Variablen>
# optional: BENCH_UPSTREAM_URL (Standard https://api.anthropic.com), BENCH_UPSTREAM_AUTH=bearer

image/build.sh [<claude-binary>]                    # baut agent-compare:local mit gepinntem claude
mkdir -p $BENCH_BASE/shared/ctx-bin && cp dist/ctx $BENCH_BASE/shared/ctx-bin/ctx   # nur ghosttree-Arm
```

Die Version von `claude` steht im Image. Wer sie festlegen will, gibt die Binärdatei an `image/build.sh`;
das Image steht mit seiner ID im Isolationsprotokoll.

Vor einer Messung die verdeckten Checks prüfen: Die Referenzlösung muss
bestehen, die naive Lösung muss `know` verfehlen.

```bash
python3 hidden/validate_ledgerly.py
```

Und die Isolation prüfen, ohne das Modell aufzurufen:

```bash
cd harness
python3 bench.py probe        # baut eine Wegwerf-Kette, probt vor und nach `claude --version`, schreibt isolation.json
make bench-test               # Tests; die mit Docker oder bwrap sind sonst übersprungen
```

## Messen

```bash
cd harness
python3 bench.py chain ghosttree 0                 # eine Pilotkette (Wiederholung 0 wird nicht gezählt)
python3 run_campaign.py --ledgerly-reps 8 --nurlearn-reps 0 --max-par 12
python3 analyze.py                                 # Tabelle, Kontraste, Mechanismus
python3 analyze.py --project nurlearn
python3 bench.py regrade <kette>                   # nur bei Harness-/Umgebungsfehlern
python3 bench.py cleanup                           # eigene, verwaiste Sandbox-Container nach Label entfernen
python3 bench.py cleanup <kette>                   # alle eigenen Container dieser Kette
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
- **Unix-Sockets:** In der Sandbox ist `/tmp` ein kurzer tmpfs (`exec`, damit Go-Testbinaries laufen),
  weil Tests Sockets darunter binden (`sun_path` ≤ 108 Byte). Der Socket des Proxys liegt unter
  `$BENCH_RUNS/<kette>/sock/`.
- **Aufräumen nach Kennung:** Container tragen die Labels `bench.run=<kette>`, `bench.owner=<uid>` und
  `bench.pid=<harness-prozess>` und werden danach entfernt, nie über ein Muster in Prozessen oder Umgebungen.
  `bench.py cleanup` ohne Kennung entfernt nur eigene Container, deren Harness nicht mehr läuft.
- **Modellidentität:** Ein Aufruf zählt nur, wenn das Hauptmodell in
  `modelUsage` ein `canonicalModel` mit Präfix `claude-opus-5-5` hat.
  Helfer sind nur Haiku 4.5/5.5 und Sonnet 5.5.
