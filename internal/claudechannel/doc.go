// Package claudechannel bringt Koordinationsnachrichten in eine Claude-Code-
// Session, die beim Start mit einem Channel gestartet wurde.
//
// Gemessen am 2026-09-30 mit Claude Code 2.1.284 (Wissenseintrag #2359): ein
// MCP-Server, der capabilities.experimental["claude/channel"] deklariert und
// notifications/claude/channel mit {content, meta} sendet, weckt eine wartende
// Session und wird in einer arbeitenden am nächsten Tool-Ergebnis absorbiert.
// Das gilt nur als Opt-in beim Start; eine laufende Session lässt sich nicht
// nachträglich anbinden (siehe OptInAtStartOnly).
//
// Zustellung ist AT-MOST-ONCE, und das ist eine bekannte Grenze, kein Zufall.
// Je Nachricht gilt die Reihenfolge Claim, dann Notification, dann Cursor. Der
// Claim ist atomar im Server, damit zwei Poller derselben Session nicht beide
// zustellen. Ein gewonnener Claim, dessen Notification nie rausgeht (der
// Prozess stirbt dazwischen, der Transport ist weg), ist verloren: der Claim
// wird nicht zurückgegeben, die Nachricht wird nie erneut zugestellt, und der
// Pull-Pfad (coord_inbox, coord_dm_read) blendet sie ebenfalls aus, weil er
// geclaimte Nachrichten als eingebracht behandelt. Das ist der Preis dafür,
// dass nie doppelt zugestellt wird; die Alternative wäre eine Nachricht, die
// eine Session zweimal weckt.
//
// Der Cursor ist der gemeinsame Lesestand des Pull-Pfads und wird deshalb nur
// bis zur ersten Nachricht fortgeschrieben, die der Poller NICHT zugestellt hat
// (gewöhnlicher Raumverkehr ohne Erwähnung). Alles davor ist zugestellt oder
// eigene Post; alles danach bleibt für coord_inbox lesbar.
package claudechannel
