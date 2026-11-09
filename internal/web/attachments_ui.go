package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Bilder an Ideen und Varianten im Web (REQ-611 K12). Die Auslieferung ist eine
// eigene Route, weil die Browsersitzung nicht über die Token-Schnittstelle liest:
// sie prüft dieselben Rechte wie der Store (AttachmentData) und antwortet mit dem
// Typ aus den ersten Bytes, nosniff und einer Richtlinie, die nichts außer dem
// Bild zulässt. Hochgeladen wird mit einem Formular (CSRF, interaktive Sitzung);
// den Typ bestimmen die Bytes, nie die Angabe des Browsers, Größe und Maße
// prüft internal/idea.

func init() {
	mockIcons["image"] = `<rect x="2.2" y="3" width="11.6" height="10" rx="1.4"/><circle cx="5.8" cy="6.4" r="1.1"/><path d="m2.6 12 3.4-3.2 2.4 2.2 2-1.8 3 2.8"/>`
}

// maxUploadForm ist die Grenze eines Formulars mit Bild: das Bild plus Kopf und Felder.
const maxUploadForm = idea.MaxAttachmentBytes + 256<<10

// attachImage ist ein Bild für die Seite.
type attachImage struct {
	Src, Alt, Who string
	W, H          int
}

// attachImages macht aus den Anhängen die Bilder, die zu einem Eigentümer gehören.
func attachImages(l lang, metas []idea.AttachmentMeta, keepOwner func(idea.AttachmentMeta) bool) []attachImage {
	var out []attachImage
	for _, m := range metas {
		if !keepOwner(m) {
			continue
		}
		alt := m.Alt
		if alt == "" {
			alt = l.msg("workshop.img_alt", firstNonEmpty(m.Person, "?"))
		}
		out = append(out, attachImage{Src: attachmentSrc(m.ID), Alt: alt, Who: m.Person, W: m.Width, H: m.Height})
	}
	return out
}

// attachmentSrc ist die Adresse eines Bildes auf der Weboberfläche.
func attachmentSrc(id int64) string { return "/ui/attachments/" + strconv.FormatInt(id, 10) }

// attachmentImage liefert die Bytes eines Anhangs. Wer ihn nicht lesen darf, bekommt
// dieselbe Antwort wie bei einer Nummer, die es nicht gibt.
func (a *app) attachmentImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	meta, data, err := a.access(r).AttachmentData(id)
	if err != nil {
		if errors.Is(err, store.ErrAccessForbidden) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
		return
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[meta.Mime]
	if ext == "" {
		http.Error(w, "unknown type", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", meta.Mime)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="attachment-%d.%s"`, meta.ID, ext))
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("ETag", `"`+meta.SHA256+`"`)
	if r.Header.Get("If-None-Match") == h.Get("ETag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// attachFailKeys sind die Regelcodes des Bildes mit dem Schlüssel, der in der
// Adresse der Rückkehr steht (nie ein Text aus der Anfrage).
var attachFailCodes = map[string]string{
	"attachment_empty": "img_none", "attachment_type": "img_type", "attachment_type_mismatch": "img_type",
	"attachment_too_large": "img_size", "attachment_dimensions": "img_size", "attachment_invalid": "img_bad",
}

// ideaAttach ist POST /ui/ideas/{id}/attach (file, alt, owner idea|variant,
// variant): hängt ein Bild an die Idee oder an eine ihrer Varianten. Wer an der
// Idee mitarbeiten darf (ab member), darf es; was er nicht lesen darf, ist 404.
func (a *app) ideaAttach(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	i, err := pa.IdeaView(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	back := repoHref(i.Project) + "/ideas/" + strconv.FormatInt(id, 10)
	owner := r.FormValue("owner")
	var variantID int64
	switch owner {
	case idea.OwnerIdea:
	case idea.OwnerVariant:
		if variantID, err = strconv.ParseInt(r.FormValue("variant"), 10, 64); err != nil || variantID <= 0 {
			http.Error(w, "invalid input", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, back+"?fail=img_none#images", http.StatusSeeOther)
		return
	}
	defer file.Close()
	// One byte more than allowed, so that a file over the limit is told from one at it.
	data, err := io.ReadAll(io.LimitReader(file, idea.MaxAttachmentBytes+1))
	if err != nil {
		http.Redirect(w, r, back+"?fail=img_bad#images", http.StatusSeeOther)
		return
	}
	// DeclaredType stays empty on purpose: what the browser says the file is does not count.
	_, err = pa.Attach(store.AttachInput{Subject: idea.SubjectIdea, SubjectID: id, Owner: owner, VariantID: variantID, Data: data,
		Alt: r.FormValue("alt"), Actor: store.IdeaActorFor(browserPrincipal(r), "")})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAccessForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, store.ErrAccessNotFound):
			http.NotFound(w, r)
		default:
			code := requestdomain.Code(err)
			if short, ok := attachFailCodes[code]; ok {
				http.Redirect(w, r, back+"?fail="+url.QueryEscape(short)+"#images", http.StatusSeeOther)
				return
			}
			if code != "" {
				http.Redirect(w, r, back+"?fail=img_other#images", http.StatusSeeOther)
				return
			}
			http.Error(w, "could not save", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, back+"?done=image#images", http.StatusSeeOther)
}
