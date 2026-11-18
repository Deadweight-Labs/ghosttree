package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/Deadweight-Labs/ghosttree/internal/llm"
	"github.com/Deadweight-Labs/ghosttree/internal/secretbox"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// The form of a connection (S05): a refused form is shown again on the same
// page with everything the person typed and the reason at the field. The key
// is the one value that is never shown again, also not after a refusal: the
// field comes back empty and says so.

// aiModelRow is one model of the form: name and the two prices as typed.
type aiModelRow struct {
	Name, In, Out string
	// Err is what is wrong with this row.
	Err string
}

// aiFormView is a connection form, new or edited, with what a refusal found.
type aiFormView struct {
	ID                         int64
	Server                     bool
	Name, Format, BaseURL, Cap string
	Models                     []aiModelRow
	FromFile                   bool
	Last4                      string
	// Errors are the reasons by field: name, base_url, key, models, cap, form.
	Errors map[string]string
	// KeyAgain: the refused form carried a key; it is not handed back.
	KeyAgain bool
}

// Open says the form shows a refusal and therefore stands open.
func (f aiFormView) Open() bool { return len(f.Errors) > 0 }

// Focus names the first field with a reason, for the cursor.
func (f aiFormView) Focus() string {
	for _, k := range []string{"name", "base_url", "key", "models", "cap"} {
		if f.Errors[k] != "" {
			return k
		}
	}
	return ""
}

// Err is the reason at a field.
func (f aiFormView) Err(field string) string { return f.Errors[field] }

// ModelRows is what the model rows show: the rows, and always one empty row
// to type into.
func (f aiFormView) ModelRows() []aiModelRow {
	rows := f.Models
	if len(rows) == 0 || rows[len(rows)-1] != (aiModelRow{}) {
		rows = append(append([]aiModelRow{}, rows...), aiModelRow{})
	}
	return rows
}

func aiPrice(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func aiModelRows(ms []store.AIModel) []aiModelRow {
	rows := make([]aiModelRow, 0, len(ms))
	for _, m := range ms {
		rows = append(rows, aiModelRow{Name: m.Model, In: aiPrice(m.InUSD), Out: aiPrice(m.OutUSD)})
	}
	return rows
}

// aiFormOf is the form of a connection row, or of a refusal that was about it.
func aiFormOf(c aiConnRow, server bool, st *aiFormView) aiFormView {
	f := aiFormView{ID: c.ID, Server: server, Name: c.Name, Format: c.Format, BaseURL: c.BaseURL, Cap: c.Cap, Models: aiModelRows(c.Models), FromFile: c.FromFile, Last4: c.Last4}
	if st != nil && st.ID == c.ID && st.Server == server {
		kept := *st
		kept.FromFile, kept.Last4 = f.FromFile, f.Last4
		if f.FromFile {
			// Name, format and address are the file's, not the form's.
			kept.Name, kept.Format, kept.BaseURL = f.Name, f.Format, f.BaseURL
		}
		return kept
	}
	return f
}

// aiNewForm is the form for a new connection, or the refusal of one.
func aiNewForm(server bool, st *aiFormView) aiFormView {
	if st != nil && st.ID == 0 && st.Server == server {
		return *st
	}
	return aiFormView{Server: server, Format: store.AIFormatAnthropic}
}

// parseAIModelRows reads the model rows of the form: name, price in and price
// out, side by side in three lists. A row left empty is skipped. The old
// single text field (one model per line) is still read when no row came.
func parseAIModelRows(r *http.Request, l lang) ([]store.AIModel, []aiModelRow, string) {
	names, ins, outs := r.Form["model_name"], r.Form["model_in"], r.Form["model_out"]
	if len(names) == 0 && len(ins) == 0 && len(outs) == 0 {
		ms, err := parseAIModels(r.FormValue("models"))
		if err != nil {
			return nil, nil, l.msg("ai.f.models.empty")
		}
		return ms, aiModelRows(ms), ""
	}
	at := func(s []string, i int) string {
		if i < len(s) {
			return strings.TrimSpace(s[i])
		}
		return ""
	}
	var rows []aiModelRow
	var models []store.AIModel
	seen := map[string]bool{}
	bad := false
	for i := range max(len(names), len(ins), len(outs)) {
		row := aiModelRow{Name: at(names, i), In: at(ins, i), Out: at(outs, i)}
		if row == (aiModelRow{}) {
			continue
		}
		in, errIn := strconv.ParseFloat(strings.ReplaceAll(row.In, ",", "."), 64)
		out, errOut := strconv.ParseFloat(strings.ReplaceAll(row.Out, ",", "."), 64)
		switch {
		case row.Name == "":
			row.Err = l.msg("ai.f.model.name_missing")
		case len(row.Name) > 100 || strings.ContainsAny(row.Name, " \t\r\n/"):
			row.Err = l.msg("ai.f.model.name")
		case seen[row.Name]:
			row.Err = l.msg("ai.f.model.dup")
		case errIn != nil || errOut != nil || in < 0 || out < 0 || in > 10000 || out > 10000:
			row.Err = l.msg("ai.f.model.price")
		}
		seen[row.Name] = true
		bad = bad || row.Err != ""
		rows = append(rows, row)
		models = append(models, store.AIModel{Model: row.Name, InUSD: in, OutUSD: out})
	}
	switch {
	case len(rows) == 0:
		return nil, rows, l.msg("ai.f.models.empty")
	case len(rows) > 20:
		return nil, rows, l.msg("ai.f.models.many")
	case bad:
		return nil, rows, l.msg("ai.f.models.rows")
	}
	return models, rows, ""
}

// checkAIAddress says why an address is not one a connection may have, in
// words for the field; "" when it is fine. The server's connections may use
// http to this machine; those of an organization or a profile only https. The
// address a name resolves to is checked when the connection is called (it must
// be a public one) and shows in "Test connection", not here.
func checkAIAddress(l lang, raw string, server bool) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return l.msg("ai.f.url.missing")
	case err != nil || u.Host == "" || u.Scheme == "":
		return l.msg("ai.f.url.invalid")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return l.msg("ai.f.url.parts")
	}
	if server {
		if llm.CheckBaseURL("connection", u.Scheme+"://"+u.Host) != nil {
			return l.msg("ai.f.url.https_server")
		}
		return ""
	}
	if u.Scheme != "https" {
		return l.msg("ai.f.url.https")
	}
	return ""
}

func aiNameProblem(l lang, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return l.msg("ai.f.name.missing")
	}
	if len([]rune(name)) > 40 {
		return l.msg("ai.f.name.long")
	}
	for _, r := range name {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.') {
			return l.msg("ai.f.name.chars")
		}
	}
	return ""
}

// aiCapProblem words a cap that is no number.
func aiCapProblem(l lang, r *http.Request, name string) (float64, string) {
	v, err := formUSD(r, name)
	if err != nil || v > 1000000 {
		return 0, l.msg("ai.f.cap")
	}
	return v, ""
}

// aiFormFail shows the refused form on its page: the same settings page, the
// section opened, the form open with its values, the reason at the field.
func (a *app) aiFormFail(w http.ResponseWriter, r *http.Request, o store.Org, f aiFormView, status int) {
	f.Errors = nonEmpty(f.Errors)
	open := "ai"
	if f.Server {
		open = "ai-server"
	}
	a.renderOrgs(w, r, status, orgsView{Open: open, AIForm: &f}, o.Slug, "")
}

func nonEmpty(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// aiStoreFieldError sorts a refusal of the store to the field it is about;
// ok is false for one that belongs to no field.
func aiStoreFieldError(l lang, err error) (field, msg string, status int, ok bool) {
	switch {
	case errors.Is(err, store.ErrAINameTaken):
		return "name", l.msg("ai.err.name_taken"), http.StatusConflict, true
	case errors.Is(err, store.ErrAIKeyMissing):
		return "key", l.msg("ai.f.key.missing"), http.StatusBadRequest, true
	case errors.Is(err, secretbox.ErrNoKey):
		return "form", l.msg("ai.err.no_secret"), http.StatusConflict, true
	case errors.Is(err, store.ErrAIConnection):
		return "form", l.msg("ai.f.form"), http.StatusBadRequest, true
	}
	return "", "", 0, false
}
