package web

import (
	"bytes"
	"encoding/json"
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

// attachBox is the box of the pictures on an idea, an issue or a pain: the pictures,
// and the form that adds one for whoever may work on the object (CanAttach).
type attachBox struct {
	Images    []attachImage
	CanAttach bool
	// Action is where the form posts, Owner what the picture belongs to (the object
	// itself: idea, request or pain).
	Action, Owner, Token string
}

// MaxBytes is the size a picture may have, for the script that checks it before the upload.
func (attachBox) MaxBytes() int { return idea.MaxAttachmentBytes }

// attachBoxFor builds the box of an object: its pictures that belong to the object itself,
// and the form when the viewer may work on it and the page is an interactive one.
func (a *app) attachBoxFor(r *http.Request, l lang, pa *store.ProjectAccess, subject string, id int64, action string) *attachBox {
	box := &attachBox{Action: action, Owner: subject, Token: csrfOf(r)}
	if metas, err := pa.AttachmentsOf(subject, id); err == nil {
		box.Images = attachImages(l, metas, func(m idea.AttachmentMeta) bool { return m.Owner == subject })
	}
	box.CanAttach = interactive(r) && pa.CanAttach(subject, id)
	return box
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
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'none'; sandbox; frame-ancestors 'none'")
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
	a.attachUpload(w, r, pa, store.AttachInput{Subject: idea.SubjectIdea, SubjectID: id, Owner: owner, VariantID: variantID}, back)
}

// requestAttach ist POST /ui/requests/{id}/attach (file, alt): hängt ein Bild an das
// Issue selbst (REQ-671). Wer daran mitarbeiten darf, darf es; ein Gast bekommt 403,
// ein Issue, das er nicht lesen darf, ist 404.
func (a *app) requestAttach(w http.ResponseWriter, r *http.Request) {
	detail, ok := a.lookupRequest(w, r, store.ActWork)
	if !ok {
		return
	}
	back := requestURL(detail.Request.ID)
	if detail.Request.Scope.Project != "" {
		back = repoHref(detail.Request.Scope.Project) + "/issues/" + strconv.FormatInt(detail.Request.ID, 10)
	}
	a.attachUpload(w, r, a.access(r), store.AttachInput{Subject: idea.SubjectRequest, SubjectID: detail.Request.ID, Owner: idea.OwnerRequest}, back)
}

// painAttach ist POST /ui/pains/{id}/attach (file, alt): hängt ein Bild an den Pain
// (REQ-671). Wie beim Issue: Mitarbeit nötig, ein verborgener Pain ist 404.
func (a *app) painAttach(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	pa := a.access(r)
	p, err := a.store.PainByID(id)
	if err != nil {
		pa.Filtered()
		http.NotFound(w, r)
		return
	}
	if err := pa.CheckPain(p, store.ActRead); err != nil {
		a.painFail(w, r, err)
		return
	}
	a.attachUpload(w, r, pa, store.AttachInput{Subject: idea.SubjectPain, SubjectID: id, Owner: idea.OwnerPain}, repoHref(p.Project)+"/pains/"+strconv.FormatInt(id, 10))
}

// attachMessages are the texts of a refused picture for the script that uploads in the
// background (the page shows the same words after a redirect, from ?fail=img_*).
var attachMessages = map[string]string{
	"img_none": "ideas.result.img_none", "img_type": "ideas.result.img_type", "img_size": "ideas.result.img_size",
	"img_bad": "ideas.result.img_bad", "img_other": "ideas.result.img_other",
}

// writeAttachJSON answers the upload that ui-attach.js sends in the background (wantsJSON, joinpair.go):
// the new picture or the reason it was refused, instead of a redirect to a page that would be loaded
// again and take a begun comment with it.
func writeAttachJSON(w http.ResponseWriter, status int, v map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// attachRefused answers a refused picture: the redirect with fail=img_* for the form, the
// words for the script.
func (a *app) attachRefused(w http.ResponseWriter, r *http.Request, back, short string) {
	if wantsJSON(r) {
		writeAttachJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "message": a.langFor(r).msg(attachMessages[short])})
		return
	}
	http.Redirect(w, r, back+"?fail="+url.QueryEscape(short)+"#images", http.StatusSeeOther)
}

// attachUpload reads the file of the form, hands it to Attach (which checks the right
// to work on the object and the image itself) and goes back to the page: with done=image
// when it worked, with fail=img_* when the picture was refused. The type is the one the
// bytes show, never the one the browser names. Asked for JSON (the script of the page),
// it answers with the markup of the new picture instead, and the page stays as it is.
func (a *app) attachUpload(w http.ResponseWriter, r *http.Request, pa *store.ProjectAccess, in store.AttachInput, back string) {
	file, _, err := r.FormFile("file")
	if err != nil {
		a.attachRefused(w, r, back, "img_none")
		return
	}
	defer file.Close()
	// One byte more than allowed, so that a file over the limit is told from one at it.
	data, err := io.ReadAll(io.LimitReader(file, idea.MaxAttachmentBytes+1))
	if err != nil {
		a.attachRefused(w, r, back, "img_bad")
		return
	}
	// DeclaredType stays empty on purpose: what the browser says the file is does not count.
	in.Data, in.Alt, in.Actor = data, r.FormValue("alt"), store.IdeaActorFor(browserPrincipal(r), "")
	meta, err := pa.Attach(in)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAccessForbidden):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, store.ErrAccessNotFound):
			http.NotFound(w, r)
		default:
			code := requestdomain.Code(err)
			if short, ok := attachFailCodes[code]; ok {
				a.attachRefused(w, r, back, short)
				return
			}
			if code == "pain_not_found" || code == "request_not_found" {
				http.NotFound(w, r)
				return
			}
			if code != "" {
				a.attachRefused(w, r, back, "img_other")
				return
			}
			http.Error(w, "could not save", http.StatusInternalServerError)
		}
		return
	}
	if wantsJSON(r) {
		l := a.langFor(r)
		var img bytes.Buffer
		for _, im := range attachImages(l, []idea.AttachmentMeta{meta}, func(idea.AttachmentMeta) bool { return true }) {
			if err := l.page().ExecuteTemplate(&img, "wsimage", im); err != nil {
				http.Error(w, "could not render", http.StatusInternalServerError)
				return
			}
		}
		writeAttachJSON(w, http.StatusOK, map[string]any{"ok": true, "id": meta.ID, "owner": meta.Owner, "html": img.String()})
		return
	}
	http.Redirect(w, r, back+"?done=image#images", http.StatusSeeOther)
}
