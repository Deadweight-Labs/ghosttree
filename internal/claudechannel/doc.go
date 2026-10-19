// Package claudechannel bringt Koordinationsnachrichten in eine Claude-Code-
// Session, die beim Start mit einem Channel gestartet wurde.
//
// Zuerst gemessen am 2026-09-30 mit Claude Code 2.1.284 (Wissenseintrag #2359): ein
// MCP-Server, der capabilities.experimental["claude/channel"] deklariert und
// notifications/claude/channel mit {content, meta} sendet, weckt eine wartende
// Session und wird in einer arbeitenden am nächsten Tool-Ergebnis absorbiert.
// Das gilt nur als Opt-in beim Start; eine laufende Session lässt sich nicht
// nachträglich anbinden (siehe OptInAtStartOnly).
//
// Protokollrevision: Channels tragen nur die alte initialize-Verbindung.
// Gemessen am 2026-10-01 mit Claude Code 2.1.286 (Wissenseintrag #2382): Claude
// Code fragt einen stdio-Server zuerst mit server/discover an. Beantwortet der
// Server das und nennt 2026-07-28, verhandelt Claude Code die moderne Revision
// und registriert keinen Channel ("connection negotiated a modern protocol
// revision with no unsolicited notification path"). Deshalb beantwortet
// Transport.Read jedes server/discover selbst mit -32601 und reicht es nicht
// ans SDK weiter; Claude Code fällt auf initialize (2025-11-25) zurück, und der
// Channel weckt die Session. Das ist eine Eigenschaft dieser Claude-Code-
// Version und ihres Feature-Flags; ändert Claude Code das, ist die Messung neu
// zu machen (scripts/verify-claude-channel.sh).
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
// ist der Preis dafür, dass nie doppelt zugestellt wird. Dazu gehört auch ein
// Claim, der in ClaimTimeout ausläuft, nachdem der Server ihn schon verbucht
// hat: er gilt hier als "nicht geschrieben", das Budget geht zurück, und der
// nächste Lauf bekommt won=false. Die Nachricht ist dann geclaimt, aber nie
// gesendet, und im Pull-Pfad ausgeblendet. Eine weitere Grenze:
// der Write selbst läuft unter dem Datei-Lock, und eine blockierte Pipe hält
// ihn, bis sie sich löst.
//
// Weckregel für Antworten und Schleifenschutz: eine Antwort auf eine eigene
// Anfrage weckt den Fragenden (siehe ClassifyParent), damit er nicht pollen
// muss. Eine Anfrage ist eine Nachricht mit Attention-Intent (Frage, Freigabe,
// Blocker, Übergabe) oder eine Nachricht, die selbst keine Antwort ist und den
// Antwortenden ausdrücklich erwähnt; in Direkt- und Gruppenräumen, wo es keine
// Erwähnungen gibt, ist jede eigene Nachricht, die keine Antwort ist, eine
// Anfrage. Die Erwähnung, die das reply-Tool in
// Projekträumen automatisch am Absender setzt, macht eine Antwort nie zur
// Anfrage. Eine Antwort auf eine eigene Antwort oder sonstige Nicht-Anfrage,
// etwa ein Dank, weckt nicht, und die Tool-Instruktion bittet, Antworten nicht
// zu beantworten: eine Kette endet nach einer Antwort. Wer nach einer Antwort
// erneut etwas braucht (Re-Review nach einem Fix), setzt am reply einen
// Attention-Intent (attentionIntent weckt auch als Antwort) oder nutzt send mit
// Mention. Eine Schleife über
// bewusste Sends (A sendet mit Mention, B sendet mit Mention, ...) begrenzt
// zweierlei: die Tools send und reply mit Attention-Intent lassen je
// Channel-Prozess höchstens 10 solcher Nachrichten pro Minute und 30 pro 15
// Minuten zu (ctx channel, sendMentionsPer*),
// und das Empfangsbudget (hookbudget.CoordLimit, 12000 Zeichen je 5 Minuten)
// kürzt die Zustellung. Letzteres greift bei kurzen Nachrichten erst spät,
// deshalb die Sendegrenze. Der Inhalt einer Notification wird von
// "<channel" und "</channel" befreit (zu "&lt;channel"), damit ein Body keinen
// eigenen Channel-Block mit gefälschtem sender_kind vortäuscht.
//
// Autorität: meta trägt sender_role, recipient_role und authority (directive
// oder request). Der Server berechnet sie beim Lesen aus den aktuellen Rollen
// (store.AuthorityFor); der Body hat keinen Einfluss. Eine ehrliche Grenze:
// sender, sender_kind, sender_role und authority sind echt, der INHALT ist es
// nicht. Ein Lead-Agent kann durch Repository- oder Web-Inhalt gesteuert
// werden, und eine solche Injection setzt sich bei seinen Workern als
// directive fort. Directives von Agenten sind deshalb begrenzt: der Empfänger
// führt sie nur im Rahmen seines Auftrags und seiner Rechte aus und bestätigt
// vor jedem zerstörerischen, unumkehrbaren oder nach außen wirkenden Schritt
// (push, löschen, deploy, veröffentlichen, Secrets, Ausgaben) bei einem
// Menschen. Das ist eine Abmilderung durch die Instruktion, keine technische
// Sperre.
//
// Gespräche beginnen: der Channel-Server (ctx channel) bietet neben reply das
// Tool send. Es schreibt in den Projekt- oder Maschinenraum, mit Mentions und
// optionalem Intent, über dieselben Client- und Store-Wege wie coord_send. Ein
// Channel-Agent braucht dafür kein ctx mcp daneben. Eine Anfrage mit send ist
// genau die Nachricht, deren Beantwortung den Fragenden weckt (siehe oben).
//
// Der Cursor ist der gemeinsame Lesestand des Pull-Pfads und wird deshalb nur
// bis zur ersten Nachricht fortgeschrieben, die der Poller NICHT zugestellt hat
// (gewöhnlicher Raumverkehr ohne Erwähnung). Alles davor ist zugestellt oder
// eigene Post; alles danach bleibt für coord_inbox lesbar.
package claudechannel
