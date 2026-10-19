package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

// lang ist die Sprache einer Antwort. Sie wird je Request aufgelöst (langFor)
// und durch Ansichten und Vorlagen gereicht; es gibt keine globale Variable.
type lang string

const (
	langEN lang = "en"
	langDE lang = "de"
)

// catalogs sind die Textkataloge je Sprache; beide werden nur gelesen.
var catalogs = map[lang]map[string]string{langEN: messages, langDE: messagesDE}

// ParseLanguage macht aus einer Einstellung eine Sprache. Leer heißt Englisch,
// alles außer en und de ist ein Fehler.
func ParseLanguage(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "en":
		return string(langEN), nil
	case "de":
		return string(langDE), nil
	}
	return "", fmt.Errorf("unknown language %q (use en or de)", s)
}

// WithLanguage stellt die Sprache der Oberfläche ein (en oder de).
func WithLanguage(s string) Option {
	return func(a *app) {
		v, err := ParseLanguage(s)
		if err != nil {
			panic("language: " + err.Error())
		}
		a.language = lang(v)
	}
}

// langFor löst die Sprache eines Requests auf. Heute ist es die Servereinstellung;
// eine Einstellung pro Person ersetzt nur diese eine Stelle.
func (a *app) langFor(r *http.Request) lang {
	if a.language == "" {
		return langEN
	}
	return a.language
}

// msg liefert den Text zu einem Schlüssel in dieser Sprache. Fehlt er im Katalog,
// gilt der englische Text; fehlt er auch dort, kommt der Schlüssel zurück, damit
// ein Tippfehler sichtbar wird statt leer zu bleiben.
func (l lang) msg(key string, args ...any) string {
	text, ok := catalogs[l][key]
	if !ok {
		if text, ok = messages[key]; !ok {
			return key
		}
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

var (
	monthsDE   = [...]string{"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"}
	weekdaysDE = [...]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}
)

// Date schreibt ein Datum kurz: "2 Jan" bzw. "2. Jan", mit withYear auch das Jahr.
func (l lang) date(t time.Time, withYear bool) string {
	if l != langDE {
		if withYear {
			return t.Format("2 Jan 2006")
		}
		return t.Format("2 Jan")
	}
	s := fmt.Sprintf("%d. %s", t.Day(), monthsDE[t.Month()-1])
	if withYear {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s
}

// weekdayDate schreibt Wochentag und Datum: "Mon 2 Jan" bzw. "Mo 2. Jan".
func (l lang) weekdayDate(t time.Time) string {
	if l != langDE {
		return t.Format("Mon 2 Jan")
	}
	return weekdaysDE[t.Weekday()] + " " + l.date(t, false)
}

// dateTime schreibt Datum und Uhrzeit: "2 Jan 15:04" bzw. "2. Jan 15:04".
func (l lang) dateTime(t time.Time) string {
	return l.date(t, false) + " " + t.Format("15:04")
}

// templates parst alle Vorlagen einmal je Sprache; die Hilfsfunktionen der
// Vorlagen sind an ihre Sprache gebunden.
func parseTemplates(l lang) *template.Template {
	return template.Must(template.New("").Funcs(template.FuncMap{
		"t":    l.msg,
		"ago":  func(stamp string) template.HTML { return timeTag(l, stamp) },
		"lbl":  func(group, value string) string { return adminLabel(l, group, value) },
		"lang": func() string { return string(l) },
		"lead": func(s string) string { return leadSentence(s, 200) },
	}).ParseFS(files, "templates/*.html"))
}

var pages = map[lang]*template.Template{langEN: parseTemplates(langEN), langDE: parseTemplates(langDE)}

func (l lang) page() *template.Template {
	if p, ok := pages[l]; ok {
		return p
	}
	return pages[langEN]
}
