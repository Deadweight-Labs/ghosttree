package workflow

import (
	"embed"
	"strings"
)

// Templates are the workflows a person creates with one click in the tab
// Workflows (REQ-696): Robin's four examples with the alias "mittel" and a
// small budget. A template is the text of a first version; after that the
// workflow is edited like any other.

//go:embed templates/*.md
var templateFiles embed.FS

// Template is one template: its name (also the name of the workflow it
// creates) and its text.
type Template struct {
	Name, Text string
}

// TemplateNames lists the templates in the order the tab offers them.
var TemplateNames = []string{"wissen-nach-merge", "wuensche-im-pr", "idee-abgleich", "kriterien-aus-diskussion"}

// TemplateByName finds a template.
func TemplateByName(name string) (Template, bool) {
	for _, n := range TemplateNames {
		if n == name {
			raw, err := templateFiles.ReadFile("templates/" + n + ".md")
			if err != nil {
				return Template{}, false
			}
			return Template{Name: n, Text: strings.ReplaceAll(string(raw), "\r\n", "\n")}, true
		}
	}
	return Template{}, false
}

// Templates lists all templates.
func Templates() []Template {
	out := make([]Template, 0, len(TemplateNames))
	for _, n := range TemplateNames {
		if t, ok := TemplateByName(n); ok {
			out = append(out, t)
		}
	}
	return out
}
