package tui

import "strings"

// The TUI speaks German when the locale says so, English otherwise. The words
// are few on purpose: the screens should explain themselves.
var texts = map[string][2]string{ // key: {en, de}
	"search.placeholder":    {"REQ-12, PAIN-4, #4295, pr:187 or a search", "REQ-12, PAIN-4, #4295, pr:187 oder Suche"},
	"tasks":                 {"My tasks", "Meine Tasks"},
	"requests":              {"Open requests", "Offene Requests"},
	"messages":              {"Messages", "Nachrichten"},
	"results":               {"Results", "Treffer"},
	"nothing":               {"nothing", "nichts"},
	"loading":               {"loading", "lädt"},
	"settings":              {"Settings", "Einstellungen"},
	"key.open":              {"open", "öffnen"},
	"key.browser":           {"browser", "Browser"},
	"key.copy":              {"copy link", "Link kopieren"},
	"key.back":              {"back", "zurück"},
	"key.quit":              {"quit", "beenden"},
	"key.settings":          {"settings", "Einstellungen"},
	"key.section":           {"section", "Bereich"},
	"key.refresh":           {"refresh", "neu laden"},
	"key.look":              {"look", "Aussehen"},
	"key.select":            {"select", "wählen"},
	"key.run":               {"run", "ausführen"},
	"key.fix":               {"repair", "reparieren"},
	"key.login":             {"sign in", "anmelden"},
	"key.logout":            {"sign out", "abmelden"},
	"key.retry":             {"again", "nochmal"},
	"key.cancel":            {"cancel", "abbrechen"},
	"logout.confirm":        {"Sign out of %s?", "Von %s abmelden?"},
	"opened":                {"opened in the browser", "im Browser geöffnet"},
	"copied":                {"link copied", "Link kopiert"},
	"code.copied":           {"code copied", "Code kopiert"},
	"not.opened":            {"not opened", "nicht geöffnet"},
	"servers":               {"Servers", "Server"},
	"account":               {"Account", "Konto"},
	"channel":               {"Update channel", "Update-Kanal"},
	"harness":               {"Harnesses", "Harnesses"},
	"collector":             {"Collector", "Collector"},
	"machine":               {"Machine", "Maschine"},
	"this.machine":          {"This machine", "Diese Maschine"},
	"github":                {"GitHub account", "GitHub-Konto"},
	"github.web":            {"in your web profile", "im Web-Profil"},
	"default":               {"default", "Standard"},
	"reachable":             {"reachable", "erreichbar"},
	"unreachable":           {"unreachable", "nicht erreichbar"},
	"checking":              {"checking", "prüft"},
	"signed.in":             {"signed in", "angemeldet"},
	"signed.out":            {"not signed in", "nicht angemeldet"},
	"token.stored":          {"login stored", "Anmeldung gespeichert"},
	"token.valid":           {"login valid", "Anmeldung gültig"},
	"token.rejected":        {"login refused by the server", "Anmeldung vom Server abgelehnt"},
	"running":               {"running", "läuft"},
	"stopped":               {"stopped", "gestoppt"},
	"missing":               {"not installed", "nicht eingerichtet"},
	"unknown":               {"unknown", "unbekannt"},
	"restart":               {"restart", "neu starten"},
	"doctor.ok":             {"all checks pass", "alles in Ordnung"},
	"doctor.unrun":          {"not checked yet", "noch nicht geprüft"},
	"other.server":          {"Other server", "Anderer Server"},
	"login.server":          {"Server", "Server"},
	"login.machine":         {"Machine", "Maschine"},
	"login.code":            {"Code", "Code"},
	"login.waiting":         {"waiting for the browser", "wartet auf den Browser"},
	"login.done":            {"Signed in as", "Angemeldet als"},
	"login.failed":          {"Sign-in failed", "Anmeldung fehlgeschlagen"},
	"login.taken":           {"This machine name belongs to another account. Try:", "Der Maschinenname gehört einem anderen Konto. Versuch:"},
	"login.expires":         {"expires", "läuft ab"},
	"key.continue":          {"continue", "weiter"},
	"key.next":              {"next field", "nächstes Feld"},
	"no.config":             {"not connected", "nicht verbunden"},
	"version":               {"version", "Version"},
	"ref.unknown":           {"not found, or not readable for you", "nicht gefunden oder für dich nicht lesbar"},
	"no.project":            {"No project in this folder", "Kein Projekt in diesem Ordner"},
	"no.git":                {"not a git repository", "kein Git-Repository"},
	"no.remote":             {"no git remote", "kein Git-Remote"},
	"project.unknown":       {"%s is not known to ghosttree, or not visible to you", "%s ist ghosttree nicht bekannt oder für dich nicht sichtbar"},
	"btn.claim":             {"Create project from this folder", "Ordner als Projekt anlegen"},
	"btn.dir":               {"Choose another folder…", "Anderen Ordner wählen…"},
	"btn.projects":          {"Show projects", "Projekte anzeigen"},
	"folder":                {"Folder", "Ordner"},
	"projects":              {"Projects", "Projekte"},
	"problem.noproject":     {"no project in this folder", "kein Projekt in diesem Ordner"},
	"problem.forbidden":     {"no access to this project", "keine Rechte in diesem Projekt"},
	"problem.offline":       {"server not reachable", "Server nicht erreichbar"},
	"problem.signedout":     {"not signed in", "nicht angemeldet"},
	"problem.other":         {"could not be loaded", "konnte nicht geladen werden"},
	"claim.project":         {"Project", "Projekt"},
	"claim.org":             {"Organization", "Organisation"},
	"claim.question":        {"Create %s in %s?", "%s in %s anlegen?"},
	"claim.role":            {"you are %s there; creating needs owner", "du bist dort %s; Anlegen braucht Owner"},
	"claim.done":            {"%s now belongs to %s", "%s gehört jetzt zu %s"},
	"claim.no_org":          {"You belong to no organization yet; create one in the web interface", "Du gehörst noch zu keiner Organisation; leg eine in der Web-Oberfläche an"},
	"claim.not_org_owner":   {"Only owners of the organization can create projects", "Nur Owner der Organisation können Projekte anlegen"},
	"claim.not_org_member":  {"You are not a member of this organization", "Du bist nicht Mitglied dieser Organisation"},
	"claim.project_claimed": {"This project already belongs to another organization", "Das Projekt gehört schon einer anderen Organisation"},
	"claim.org_not_found":   {"Organization not found", "Organisation nicht gefunden"},
	"claim.no_remote":       {"This folder has no git remote", "Dieser Ordner hat keinen Git-Remote"},
	"mode.project":          {"Project", "Projekt"},
	"mode.inbox":            {"Inbox", "Inbox"},
	"tab.requests":          {"Requests", "Requests"},
	"tab.pains":             {"Pains", "Pains"},
	"tab.tasks":             {"Tasks", "Tasks"},
	"tab.messages":          {"Messages", "Nachrichten"},
	"tab.questions":         {"Questions", "Fragen"},
	"tab.suggestions":       {"Suggestions", "Vorschläge"},
	"key.create":            {"create", "anlegen"},
	"key.complete":          {"complete", "ergänzen"},
	"dirs.recent":           {"Recent", "Zuletzt genutzt"},
	"dirs.placeholder":      {"path, tab completes", "Pfad, Tab ergänzt"},
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
