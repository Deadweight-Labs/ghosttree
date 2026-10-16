#!/usr/bin/env bash
# Moves the six pains of knowledge entry #936 into single pain entries.
#
#   scripts/migrate-pains-936.sh [--source ID] [--project P] [--p3-request N] [--dry-run]
#
# Run it with ctx logged in against the target server, after the server with
# the pain type is rolled out. It does not talk to any server by itself. Safe to
# run twice: a pain whose title already exists is skipped, and the note is only
# written while the source entry is still active.
set -euo pipefail

SOURCE=936
PROJECT=""
P3_REQUEST=98
DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --source) SOURCE="$2"; shift 2 ;;
    --project) PROJECT="$2"; shift 2 ;;
    --p3-request) P3_REQUEST="$2"; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

CTX=${CTX:-ctx}
project_flag=()
[ -n "$PROJECT" ] && project_flag=(--project "$PROJECT")

existing=$("$CTX" pain list --state all --json "${project_flag[@]}")
has_title() { printf '%s' "$existing" | jq -e --arg t "$1" 'any(.data[]; .title == $t)' >/dev/null; }

add_pain() { # title body [extra args...]
  local title="$1" body="$2"
  shift 2
  if has_title "$title"; then
    echo "skip   $title"
    return
  fi
  if [ "$DRY" = 1 ]; then
    echo "would  $title"
    return
  fi
  "$CTX" pain add "$title" --body "$body" "$@" "${project_flag[@]}" | head -n 1
}

add_pain "Kein Diff, und keine Fassungen" 'Eine Spec entsteht in Fassungen. „Was hat sich seit gestern geändert" beantwortet git in einer Sekunde. `UpdateKnowledge` überschreibt `body`; der vorherige Text ist danach nirgends. Die Request-Domäne hat mit `request_activity` ein Protokoll, Wissen hat keins. Für einen kurzen Pitfall ist das richtig. Für ein Dokument, das dreimal überarbeitet wird, ist es der Unterschied zwischen „nachvollziehbar" und „weg".'

add_pain "Schreiben geht nur ganz oder gar nicht" 'Ein Werkzeugaufruf trägt den `body` als eine Zeichenkette. Um einen Absatz in einem 250-Zeilen-Dokument zu ändern, muss ich das ganze Dokument neu senden — mit dem üblichen Risiko, dass beim Neuschreiben unbemerkt etwas anderes mitwandert. Für Dateien gibt es `Edit` mit alt/neu und einer harten Eindeutigkeitsprüfung; für Wissenseinträge nichts Vergleichbares. Das ist die Reibung, die ich in dieser Session tatsächlich gespürt habe, nicht eine theoretische.'

add_pain "Lesen geht auch nur ganz" '`context_search knowledge_id=X` liefert den vollständigen Eintrag. Bei einem Dokument, von dem gerade Abschnitt 8 interessiert, gehen trotzdem alle 250 Zeilen in den Kontext. Eine Datei kann man mit offset/limit anlesen und mit grep anspringen. Genau die Fähigkeit, die REQ-98 für den Ghost-Baum baut, fehlt für das Wissen selbst.' --motivates "REQ-$P3_REQUEST"

add_pain "Es gibt keinen Entwurfszustand" 'Eine Spec schreiben heißt, einen Entwurf zu haben. In git ist das eine nicht committete Datei. In ghosttree heißt Speichern: sofort im Baum, ausgeliefert, durchsuchbar. `confidence` kennt zwar `staged`, aber das ist die Warteschlange für Destilliertes und meint „noch nicht geprüft", nicht „noch nicht fertig geschrieben". Wer keinen Ort für Halbfertiges hat, schreibt es woanders hin — und woanders ist das Repo.'

add_pain "Der Bestand ist nicht als Sammlung sichtbar" '`request_search` ohne Anfrage listet den Ledger. Für Dokumente gibt es das nicht: `plan`-Einträge liegen in derselben Trefferliste wie alles andere, und die Frage „welche Specs hat dieses Projekt" hat keine Antwort ausser einer Suche, die man richtig raten muss. Ein Verzeichnis beantwortet sie mit `ls`.'

add_pain "Kein Review-Moment vor dem Wirksamwerden" 'Eine Spec im Repo geht durch einen PR: jemand liest einen Diff und kommentiert daneben. Ein Wissenseintrag ist geschrieben und dann da. `PendingKnowledge` ist eine Warteschlange für ganze Einträge aus der Destillation, kein Ort, an dem man eine ÄNDERUNG an einem bestehenden Dokument beurteilt. Solange ich allein schreibe, ist das verschmerzbar; sobald Philipp dazukommt (siehe #15), ist es keins mehr.'

[ "$DRY" = 1 ] && exit 0

# The note replaces the source entry. Only while that one is still active.
if "$CTX" knowledge show "$SOURCE" | grep -qE '^\[[^]]*\|(superseded|retired)\]'; then
  echo "skip   note: #$SOURCE is already closed"
  exit 0
fi

ids=$("$CTX" pain list --state all --json "${project_flag[@]}" | jq -r '[.data[] | select(.title as $t | ["Kein Diff, und keine Fassungen","Schreiben geht nur ganz oder gar nicht","Lesen geht auch nur ganz","Es gibt keinen Entwurfszustand","Der Bestand ist nicht als Sammlung sichtbar","Kein Review-Moment vor dem Wirksamwerden"] | index($t))] | sort_by(.id) | map("PAIN-\(.id)") | join(", ")')

body="Rahmen der sechs Schmerzen, die als einzelne Einträge festgehalten sind: $ids.

VORWEG DER EIGENE FEHLER, damit er nicht als Werkzeugmangel durchgeht: Die Superpowers-Skill nennt als Default docs/superpowers/specs/<datum>-<thema>-design.md und sagt einen Satz später \"User preferences for spec location override this default\". Unser Regeltext ist so eine Präferenz. Der Skill-Default wurde über die Projektregel gestellt. Das ist keiner der Schmerzen.

NICHT HIER, WEIL SCHON IM LEDGER: die fehlende Herkunft samt Commit-Stand und die fehlende Alterungsfreiheit stehen in REQ-102; fehlende Verlinkung zwischen Einträgen in REQ-157.

FAZIT: die sechs Punkte betreffen ausnahmslos LANGE, MEHRFACH ÜBERARBEITETE Dokumente. Für kurze Einträge ist der Baum der Datei überlegen. Textsorte statt Werkzeugmangel: eine Textsorte verhält sich anders als die, für die das Modell gebaut wurde."

"$CTX" knowledge add --type note --scope project "${project_flag[@]}" --title "Schmerzen langer Dokumente: Rahmen und Ausschlüsse" --body "$body" \
  --supersedes "$SOURCE" --reason "in sechs einzelne pains überführt"
