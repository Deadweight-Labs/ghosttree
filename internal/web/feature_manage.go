package web

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/actor"
	"github.com/Deadweight-Labs/ghosttree/internal/feature"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Features anlegen, bearbeiten und verknüpfen im Browser (REQ-712, S19-Linie
// von REQ-702): das Formular ist dasselbe für ein neues und ein vorhandenes
// Feature, die Verknüpfung mit Issues, Ideen, Wissen und Pains ein Feld, das
// „REQ-12“, „IDEA-3“, „KN-7“ oder „PAIN-4“ liest. Alle Aktionen sind
// Lead-Aktionen (manage.go).

// featureForm is the form of a feature, new or existing.
type featureForm struct {
	Project, Action, Back string
	// ID and Revision are set for an existing feature; the revision travels with
	// the form so that a feature changed meanwhile is not overwritten blind.
	ID                                          int64
	Revision                                    int
	Name, Summary, Behavior, Limits             string
	CLI, MCP, API, UI                           string
	Areas                                       string
	UsageOpen                                   bool
	Open                                        bool
	Anchor                                      string
	Title, Submit                               string
	Token, Close                                string
	MaxName, MaxSummary, MaxBehavior, MaxLimits int
	MaxUsage, MaxNote                           int
}

func newFeatureForm(token, project string) *featureForm {
	return &featureForm{Project: project, Token: token, MaxName: feature.MaxName, MaxSummary: feature.MaxSummary, MaxBehavior: feature.MaxBehavior,
		MaxLimits: feature.MaxLimits, MaxUsage: feature.MaxUsage, MaxNote: feature.MaxNote}
}

// featureLinkOption is one link a lead may take away again.
type featureLinkOption struct{ Value, Label string }

// featureManageView is what the feature page adds for the lead.
type featureManageView struct {
	Bar      manageBar
	Form     *featureForm
	Panel    string
	Result   *requestResult
	Unlinks  []featureLinkOption
	EditHref string
}

var featureRefPattern = regexp.MustCompile(`(?i)^(REQ|ISSUE|IDEA|KN|PAIN)-?\s*#?(\d+)$`)

// parseFeatureRef reads "REQ-12", "idea 3", "KN7" or "PAIN-4" as a kind and a number.
func parseFeatureRef(s string) (kind string, n int64, ok bool) {
	m := featureRefPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	switch strings.ToUpper(m[1]) {
	case "REQ", "ISSUE":
		return feature.KindRequest, n, true
	case "IDEA":
		return feature.KindIdea, n, true
	case "KN":
		return feature.KindKnowledge, n, true
	}
	return feature.KindPain, n, true
}

// featureFieldKeys names the text fields of the form for a refusal.
var featureFieldKeys = map[string]string{
	"summary": "features.f.summary", "behavior": "features.f.behavior", "limits": "features.f.limits", "cli": "features.f.cli",
	"mcp": "features.f.mcp", "api": "features.f.api", "ui": "features.f.ui", "message": "features.f.message",
}

var featureDoneKeys = map[string]string{
	"created": "features.done.created", "saved": "features.done.saved", "unchanged": "features.done.unchanged",
	"linked": "features.done.linked", "unlinked": "features.done.unlinked",
}

// featureFail is the refusal of the store in words, and the field it is about.
func featureFail(l lang, err error) (text, field string) {
	code := requestdomain.Code(err)
	switch code {
	case "secret_detected":
		return l.msg("err.write_secret"), ""
	case "name_required", "name_invalid", "feature_name_taken":
		return l.msg("features.fail.name"), "name"
	case "area_invalid", "areas_too_many":
		return l.msg("features.fail.areas"), "areas"
	case "version_changed":
		return l.msg("features.fail.changed"), ""
	case "feature_retired":
		return l.msg("features.fail.retired"), ""
	case "feature_not_found":
		return l.msg("features.fail.gone"), ""
	}
	// A text too long or not valid is "<field>_invalid".
	if f, ok := strings.CutSuffix(code, "_invalid"); ok && featureFieldKeys[f] != "" {
		return l.msg("features.fail.field", l.msg(featureFieldKeys[f])), f
	}
	return l.msg("features.fail"), ""
}

// featureFormFields reads the form into a patch that names every field.
func featureFormPatch(r *http.Request) feature.Patch {
	get := func(name string) *string {
		s := strings.TrimSpace(lineBreaks(r.FormValue(name)))
		return &s
	}
	areas := []string{}
	for _, line := range strings.FieldsFunc(lineBreaks(r.FormValue("areas")), func(c rune) bool { return c == '\n' || c == ',' }) {
		if line = strings.TrimSpace(line); line != "" {
			areas = append(areas, line)
		}
	}
	return feature.Patch{Name: get("name"), Summary: get("summary"), Behavior: get("behavior"), Limits: get("limits"),
		CLI: get("cli"), MCP: get("mcp"), API: get("api"), UI: get("ui"), Areas: &areas}
}

func featureActorOf(r *http.Request) feature.Actor {
	p := browserPrincipal(r)
	id, _ := store.AccountNumericID(p.ID)
	return feature.Actor{AccountID: id, Person: personOf(r)}
}

// featureCreate is POST /ui/features/create.
func (a *app) featureCreate(w http.ResponseWriter, r *http.Request) {
	project, pa, ok := a.leadProject(w, r, store.ResFeature, store.ActCreate)
	if !ok {
		return
	}
	l := a.langFor(r)
	f, err := pa.CreateFeature(store.CreateFeatureInput{Project: project, Content: featureFormPatch(r), Message: strings.TrimSpace(r.FormValue("message")), Actor: featureActorOf(r)})
	if err != nil {
		text, field := featureFail(l, err)
		a.failFormTo(w, r, repoHref(project)+"/features?panel=new#new", http.StatusUnprocessableEntity, text, field)
		return
	}
	a.stampActor(r, actor.Feature, f.ID)
	http.Redirect(w, r, featureLink(project, f.ID)+"?done=created", http.StatusSeeOther)
}

// featureManage loads the feature a form talks about and checks feature.write
// on it, as the API does for a new version and for links.
func (a *app) featureManage(w http.ResponseWriter, r *http.Request) (feature.Feature, *store.ProjectAccess, bool) {
	pa := a.access(r)
	id := formNumber(r.PathValue("id"))
	f, err := pa.FeatureView(id)
	if id == 0 || err != nil {
		http.NotFound(w, r)
		return feature.Feature{}, nil, false
	}
	if a.accessDenied(w, r, pa.Check(f.Project, store.ResFeature, store.ActWork, store.Object{})) {
		return feature.Feature{}, nil, false
	}
	return f, pa, true
}

// featureEdit is POST /ui/features/{id}/edit: a new version of the feature.
func (a *app) featureEdit(w http.ResponseWriter, r *http.Request) {
	f, pa, ok := a.featureManage(w, r)
	if !ok {
		return
	}
	l := a.langFor(r)
	back := featureLink(f.Project, f.ID) + "?panel=edit#edit"
	base, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("base")))
	_, changed, err := pa.UpdateFeature(f.ID, store.UpdateFeatureInput{Patch: featureFormPatch(r), Message: strings.TrimSpace(r.FormValue("message")), Base: base, Actor: featureActorOf(r)})
	if err != nil {
		text, field := featureFail(l, err)
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, text, field)
		return
	}
	done := "saved"
	if !changed {
		done = "unchanged"
	}
	http.Redirect(w, r, featureLink(f.Project, f.ID)+"?done="+done, http.StatusSeeOther)
}

// featureLinkAdd is POST /ui/features/{id}/link.
func (a *app) featureLinkAdd(w http.ResponseWriter, r *http.Request) {
	f, pa, ok := a.featureManage(w, r)
	if !ok {
		return
	}
	l := a.langFor(r)
	back := featureLink(f.Project, f.ID) + "?panel=link#link"
	kind, n, valid := parseFeatureRef(r.FormValue("ref"))
	if !valid {
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, l.msg("features.fail.ref"), "ref")
		return
	}
	rel := feature.RelAffects
	if r.FormValue("rel") == feature.RelOrigin {
		rel = feature.RelOrigin
	}
	if _, err := pa.LinkFeature(f.ID, kind, n, rel, personOf(r)); err != nil {
		text := l.msg("features.fail.link")
		if code := requestdomain.Code(err); code == "secret_detected" {
			text = l.msg("err.write_secret")
		}
		a.failFormTo(w, r, back, http.StatusUnprocessableEntity, text, "ref")
		return
	}
	http.Redirect(w, r, featureLink(f.Project, f.ID)+"?done=linked", http.StatusSeeOther)
}

// featureLinkRemove is POST /ui/features/{id}/unlink; the value is "kind:id:rel".
func (a *app) featureLinkRemove(w http.ResponseWriter, r *http.Request) {
	f, pa, ok := a.featureManage(w, r)
	if !ok {
		return
	}
	parts := strings.Split(r.FormValue("link"), ":")
	n := int64(0)
	if len(parts) == 3 {
		n = formNumber(parts[1])
	}
	if n == 0 || !slices.Contains([]string{feature.KindIdea, feature.KindRequest, feature.KindKnowledge, feature.KindPain}, parts[0]) {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	if _, err := pa.UnlinkFeature(f.ID, parts[0], n, parts[2]); err != nil {
		http.Error(w, "could not save", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, featureLink(f.Project, f.ID)+"?done=unlinked", http.StatusSeeOther)
}

// featureResult is the line a feature page shows after a form.
func featureResult(r *http.Request, l lang) *requestResult {
	if key := featureDoneKeys[r.URL.Query().Get("done")]; key != "" {
		return &requestResult{Text: l.msg(key), Kind: "done"}
	}
	return nil
}

func init() {
	add := func(key, en, de string) {
		messages[key] = en
		messagesDE[key] = de
	}
	add("features.new", "New feature", "Neues Feature")
	add("features.edit", "Edit", "Bearbeiten")
	add("features.create", "Create feature", "Feature anlegen")
	add("features.save", "Save version", "Fassung speichern")
	add("features.f.name", "Name", "Name")
	add("features.f.summary", "Summary", "Kurzfassung")
	add("features.f.behavior", "What it does", "Was es kann")
	add("features.f.limits", "Limits", "Grenzen")
	add("features.f.usage", "Usage", "Bedienung")
	add("features.f.cli", "Command line", "Kommandozeile")
	add("features.f.mcp", "MCP tools", "MCP-Werkzeuge")
	add("features.f.api", "API", "API")
	add("features.f.ui", "Web interface", "Weboberfläche")
	add("features.f.areas", "Files and folders, one per line", "Dateien und Ordner, eine je Zeile")
	add("features.f.message", "What changed?", "Was hat sich geändert?")
	add("features.link.title", "Link", "Verknüpfen")
	add("features.link.ref", "REQ-12, IDEA-3, KN-7 or PAIN-4", "REQ-12, IDEA-3, KN-7 oder PAIN-4")
	add("features.link.affects", "Changes the feature", "Ändert das Feature")
	add("features.link.origin", "Feature came from it", "Feature entstand daraus")
	add("features.link.add", "Link", "Verknüpfen")
	add("features.link.entry", "Entry", "Eintrag")
	add("features.link.rel", "Relation", "Beziehung")
	add("features.link.linked", "Linked", "Verknüpft")
	add("features.link.pick", "Take a link away …", "Verknüpfung lösen …")
	add("features.link.remove", "Unlink", "Lösen")
	add("features.done.created", "Feature created.", "Feature angelegt.")
	add("features.done.saved", "New version saved.", "Neue Fassung gespeichert.")
	add("features.done.unchanged", "Nothing changed, no new version.", "Nichts geändert, keine neue Fassung.")
	add("features.done.linked", "Linked.", "Verknüpft.")
	add("features.done.unlinked", "Link removed.", "Verknüpfung gelöst.")
	add("features.fail", "The feature could not be saved.", "Das Feature konnte nicht gespeichert werden.")
	add("features.fail.name", "The name is missing, too long or taken. A feature name is one line of a few words.", "Der Name fehlt, ist zu lang oder vergeben. Ein Featurename ist eine Zeile aus wenigen Wörtern.")
	add("features.fail.areas", "An area is a path inside the repository, like internal/store. At most 20, no .., no wildcards.", "Ein Bereich ist ein Pfad im Repository, etwa internal/store. Höchstens 20, kein .., keine Platzhalter.")
	add("features.fail.field", "%s is too long or not valid.", "%s ist zu lang oder ungültig.")
	add("features.fail.changed", "The feature changed while you were writing. Open it again and apply your change.", "Das Feature hat sich geändert, während du geschrieben hast. Öffne es neu und übertrage deine Änderung.")
	add("features.fail.retired", "A retired feature takes no new version.", "Ein eingestelltes Feature nimmt keine neue Fassung auf.")
	add("features.fail.gone", "This feature does not exist any more.", "Dieses Feature gibt es nicht mehr.")
	add("features.fail.ref", "Write the number with its kind: REQ-12, IDEA-3, KN-7 or PAIN-4.", "Schreib die Nummer mit ihrer Art: REQ-12, IDEA-3, KN-7 oder PAIN-4.")
	add("features.fail.link", "There is no such entry in this repository, or you may not read it.", "Diesen Eintrag gibt es in diesem Repository nicht, oder du darfst ihn nicht lesen.")
}
