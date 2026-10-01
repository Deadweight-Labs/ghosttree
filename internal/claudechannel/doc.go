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
// zustellen, und er wird nie zurückgenommen: die Zustände bleiben monoton.
//
// Deshalb claimt der Poller erst, wenn die Zustellung gesichert ist: der
// Notifier ist bereit (Handshake abgeschlossen, nicht geschlossen) und das
// Koordinationsbudget ist reserviert. Der Claim läuft innerhalb von
// hookbudget.DeliverChannel, direkt vor dem Schreiben. Ist der Notifier nicht
// bereit, das Budget erschöpft oder sein Konto nicht lesbar, gibt es keinen
// Claim, die Position bleibt stehen, und der Poller geht in den Backoff.
//
// Nur ein tatsächlicher Schreibversuch verbraucht Budget: scheitert der Claim
// oder geht er an einen anderen Poller, gibt DeliverChannel die Reservierung
// unter demselben Lock zurück (hookbudget.ErrNotEmitted). Der Claim hat eine
// eigene kurze Frist (ClaimTimeout), weil er den Datei-Lock des Budgets hält.
//
// Übrig bleibt genau ein Verlustfall: ein gewonnener Claim, dessen Write
// scheitert oder dessen Prozess dazwischen stirbt. Diese Nachricht wird nie
// erneut zugestellt, und der Pull-Pfad (coord_inbox, coord_dm_read) blendet sie
// ebenfalls aus, weil er geclaimte Nachrichten als eingebracht behandelt. Das
// ist der Preis dafür, dass nie doppelt zugestellt wird. Eine weitere Grenze:
// der Write selbst läuft unter dem Datei-Lock, und eine blockierte Pipe hält
// ihn, bis sie sich löst.
//
// Der Cursor ist der gemeinsame Lesestand des Pull-Pfads und wird deshalb nur
// bis zur ersten Nachricht fortgeschrieben, die der Poller NICHT zugestellt hat
// (gewöhnlicher Raumverkehr ohne Erwähnung). Alles davor ist zugestellt oder
// eigene Post; alles danach bleibt für coord_inbox lesbar.
package claudechannel
