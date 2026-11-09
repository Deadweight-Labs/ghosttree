package tui

import "strings"

// The TUI speaks German when the locale says so, English otherwise. The words
// are few on purpose: the screens should explain themselves.
var texts = map[string][2]string{ // key: {en, de}
	"search.placeholder": {"REQ-12, PAIN-4, #4295, pr:187 or a search", "REQ-12, PAIN-4, #4295, pr:187 oder Suche"},
	"tasks":              {"My tasks", "Meine Tasks"},
	"requests":           {"Open requests", "Offene Requests"},
	"messages":           {"Messages", "Nachrichten"},
	"results":            {"Results", "Treffer"},
	"nothing":            {"nothing", "nichts"},
	"loading":            {"loading", "lädt"},
	"settings":           {"Settings", "Einstellungen"},
	"key.open":           {"open", "öffnen"},
	"key.browser":        {"browser", "Browser"},
	"key.copy":           {"copy link", "Link kopieren"},
	"key.back":           {"back", "zurück"},
	"key.quit":           {"quit", "beenden"},
	"key.settings":       {"settings", "Einstellungen"},
	"key.section":        {"section", "Bereich"},
	"key.refresh":        {"refresh", "neu laden"},
	"key.look":           {"look", "Aussehen"},
	"key.select":         {"select", "wählen"},
	"key.run":            {"run", "ausführen"},
	"key.fix":            {"repair", "reparieren"},
	"key.login":          {"sign in", "anmelden"},
	"key.logout":         {"sign out", "abmelden"},
	"key.retry":          {"again", "nochmal"},
	"key.cancel":         {"cancel", "abbrechen"},
	"logout.confirm":     {"Sign out of %s?", "Von %s abmelden?"},
	"opened":             {"opened in the browser", "im Browser geöffnet"},
	"copied":             {"link copied", "Link kopiert"},
	"code.copied":        {"code copied", "Code kopiert"},
	"not.opened":         {"not opened", "nicht geöffnet"},
	"servers":            {"Servers", "Server"},
	"account":            {"Account", "Konto"},
	"channel":            {"Update channel", "Update-Kanal"},
	"harness":            {"Harnesses", "Harnesses"},
	"collector":          {"Collector", "Collector"},
	"machine":            {"Machine", "Maschine"},
	"this.machine":       {"This machine", "Diese Maschine"},
	"github":             {"GitHub account", "GitHub-Konto"},
	"github.web":         {"in your web profile", "im Web-Profil"},
	"default":            {"default", "Standard"},
	"reachable":          {"reachable", "erreichbar"},
	"unreachable":        {"unreachable", "nicht erreichbar"},
	"checking":           {"checking", "prüft"},
	"signed.in":          {"signed in", "angemeldet"},
	"signed.out":         {"not signed in", "nicht angemeldet"},
	"token.stored":       {"login stored", "Anmeldung gespeichert"},
	"token.valid":        {"login valid", "Anmeldung gültig"},
	"token.rejected":     {"login refused by the server", "Anmeldung vom Server abgelehnt"},
	"running":            {"running", "läuft"},
	"stopped":            {"stopped", "gestoppt"},
	"missing":            {"not installed", "nicht eingerichtet"},
	"unknown":            {"unknown", "unbekannt"},
	"restart":            {"restart", "neu starten"},
	"doctor.ok":          {"all checks pass", "alles in Ordnung"},
	"doctor.unrun":       {"not checked yet", "noch nicht geprüft"},
	"other.server":       {"Other server", "Anderer Server"},
	"login.server":       {"Server", "Server"},
	"login.machine":      {"Machine", "Maschine"},
	"login.code":         {"Code", "Code"},
	"login.waiting":      {"waiting for the browser", "wartet auf den Browser"},
	"login.done":         {"Signed in as", "Angemeldet als"},
	"login.failed":       {"Sign-in failed", "Anmeldung fehlgeschlagen"},
	"login.taken":        {"This machine name belongs to another account. Try:", "Der Maschinenname gehört einem anderen Konto. Versuch:"},
	"login.expires":      {"expires", "läuft ab"},
	"key.continue":       {"continue", "weiter"},
	"key.next":           {"next field", "nächstes Feld"},
	"no.config":          {"not connected", "nicht verbunden"},
	"version":            {"version", "Version"},
	"ref.unknown":        {"not found, or not readable for you", "nicht gefunden oder für dich nicht lesbar"},
}

type lang int

const (
	langEN lang = iota
	langDE
)

// detectLang reads LC_ALL, LC_MESSAGES and LANG in that order.
func detectLang(getenv func(string) string) lang {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(k); v != "" {
			if strings.HasPrefix(strings.ToLower(v), "de") {
				return langDE
			}
			return langEN
		}
	}
	return langEN
}

func (l lang) t(key string) string {
	if v, ok := texts[key]; ok {
		return v[l]
	}
	return key
}
