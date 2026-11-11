package store

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
)

func TestAttachmentIsCheckedStoredAndServedOnlyToThoseWhoMayReadTheIdea(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Mit Bild")
	data := testPNG(t, 8)
	meta, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerIdea, Data: data, DeclaredType: "image/png", Alt: "Skizze", Actor: w.actor("mia")})
	if err != nil || meta.Mime != "image/png" || meta.Width != 8 || meta.Bytes != len(data) || meta.URL != "/api/attachments/1" || meta.Alt != "Skizze" {
		t.Fatalf("%+v %v", meta, err)
	}
	m, got, err := w.pa["gus"].AttachmentData(meta.ID)
	if err != nil || !bytes.Equal(got, data) || m.SHA256 == "" {
		t.Fatalf("a guest reads the image: %v", err)
	}
	// Unbekannt und fremd antworten gleich.
	_, _, errStranger := w.pa["nora"].AttachmentData(meta.ID)
	_, _, errMissing := w.pa["nora"].AttachmentData(meta.ID + 50)
	if ruleCode(errStranger) != "attachment_not_found" || ruleCode(errMissing) != "attachment_not_found" ||
		strings.ReplaceAll(errStranger.Error(), "ATT-1", "ATT-N") != strings.ReplaceAll(errMissing.Error(), "ATT-51", "ATT-N") {
		t.Fatalf("%v / %v", errStranger, errMissing)
	}
	if _, err := w.pa["nora"].AttachmentsOf(idea.SubjectIdea, i.ID); ruleCode(err) != "idea_not_found" {
		t.Fatalf("a stranger lists: %v", err)
	}
	// Ein Gast hängt nichts an, ein Fremder auch nicht.
	for _, who := range []string{"gus", "nora"} {
		if _, err := w.pa[who].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerIdea, Data: data, Actor: w.actor(who)}); err == nil {
			t.Errorf("%s attached an image", who)
		}
	}
	// Noch einmal dasselbe Bild am selben Platz: kein zweiter Anhang.
	again, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerIdea, Data: data, Actor: w.actor("mia")})
	if err != nil || again.ID != meta.ID {
		t.Fatalf("%+v %v", again, err)
	}
	list, _ := w.pa["gus"].AttachmentsOf(idea.SubjectIdea, i.ID)
	if len(list) != 1 {
		t.Fatalf("%+v", list)
	}
	ws, _ := w.pa["gus"].IdeaWorkshop(i.ID)
	if len(ws.Attachments) != 1 {
		t.Fatalf("the workshop lists the attachment: %+v", ws.Attachments)
	}
	// Nur Owner und Maintainer löschen.
	if err := w.pa["rex"].RemoveAttachment(meta.ID); !errors.Is(err, ErrAccessForbidden) {
		t.Fatalf("%v", err)
	}
	if err := w.pa["lena"].RemoveAttachment(meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.pa["gus"].AttachmentData(meta.ID); ruleCode(err) != "attachment_not_found" {
		t.Fatalf("%v", err)
	}
}

func TestAttachmentRefusesWhatIsNotARasterImage(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Streng")
	for name, data := range map[string][]byte{
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"html": []byte("<script>alert(1)</script>"),
		"big":  append(testPNG(t, 2), bytes.Repeat([]byte{1}, idea.MaxAttachmentBytes)...),
	} {
		if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerIdea, Data: data, DeclaredType: "image/png", Actor: w.actor("mia")}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerIdea, Data: testPNG(t, 2), DeclaredType: "image/jpeg", Actor: w.actor("mia")}); ruleCode(err) != "attachment_type_mismatch" {
		t.Fatalf("a PNG declared as JPEG: %v", err)
	}
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerIdea, Data: testPNG(t, 2), Alt: "a\nb", Actor: w.actor("mia")}); err == nil {
		t.Fatal("a multi-line description")
	}
	var n int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rejected images were stored", n)
	}
}

func TestAttachmentOwnersAreVariantsAndRepliesOfThatIdeaOnly(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Besitzer")
	other := w.workshopIdea(t, "Andere Idee")
	d := w.openOn(t, "mia", idea.SubjectIdea, i.ID, "Welches Bild", w.actor("mia"), withVariants(idea.VariantInput{Title: "A"}, idea.VariantInput{Title: "B"}))
	od := w.openOn(t, "mia", idea.SubjectIdea, other.ID, "Fremde Frage", w.actor("mia"), withVariants(idea.VariantInput{Title: "A"}, idea.VariantInput{Title: "B"}))
	png := testPNG(t, 3)
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerVariant, VariantID: od.Variants[0].ID, Data: png, Actor: w.actor("mia")}); ruleCode(err) != "variant_not_found" {
		t.Fatalf("a variant of another idea: %v", err)
	}
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerVariant, VariantID: 9999, Data: png, Actor: w.actor("mia")}); ruleCode(err) != "variant_not_found" {
		t.Fatalf("a missing variant: %v", err)
	}
	if _, err := w.pa["rex"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerVariant, VariantID: d.Variants[1].ID, Data: png, Alt: "B", Actor: w.agent("rex")}); err != nil {
		t.Fatalf("a member attaches to a variant: %v", err)
	}
	view, _ := w.pa["gus"].DecisionView(d.ID)
	if len(view.Variants[1].Attachments) != 1 || len(view.Variants[0].Attachments) != 0 {
		t.Fatalf("the image belongs to variant B: %+v", view.Variants)
	}
	// Eine Antwort: die Diskussion muss an der Idee hängen.
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerReply, ThreadID: 77, Data: png, Actor: w.actor("mia")}); ruleCode(err) != "thread_not_found" {
		t.Fatalf("a reply in a discussion that is not on the idea: %v", err)
	}
	tid, err := w.st.CreateThread(Thread{Project: roleProject, Title: "Diskussion"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.LinkThread(ThreadLink{ThreadID: tid, Kind: "idea", ID: "IDEA-1"}); err != nil {
		t.Fatal(err)
	}
	reply, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerReply, ThreadID: tid, Data: png, Actor: w.actor("mia")})
	if err != nil || reply.Owner != idea.OwnerReply {
		t.Fatalf("%+v %v", reply, err)
	}
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: other.ID, Owner: idea.OwnerReply, ThreadID: tid, Data: png, Actor: w.actor("mia")}); ruleCode(err) != "thread_not_found" {
		t.Fatalf("the same discussion on another idea: %v", err)
	}
	// Nach dem Schließen nimmt eine Variante keine Bilder mehr an.
	w.pa["mia"].CloseDecision(CloseDecisionInput{ID: d.ID, Outcome: idea.DecisionDropped, Reason: "weg", Actor: w.actor("mia")})
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerVariant, VariantID: d.Variants[0].ID, Data: testPNG(t, 5), Actor: w.actor("mia")}); ruleCode(err) != "variant_not_found" {
		t.Fatalf("a closed decision takes images: %v", err)
	}
}

func TestAttachmentsPerIdeaAreLimited(t *testing.T) {
	w := newPainWorld(t)
	i := w.workshopIdea(t, "Limit")
	for n := 0; n < idea.MaxAttachmentPerSubject; n++ {
		if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerIdea, Data: testPNG(t, 2+n), Actor: w.actor("mia")}); err != nil {
			t.Fatalf("%d: %v", n, err)
		}
	}
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerIdea, Data: testPNG(t, 99), Actor: w.actor("mia")}); ruleCode(err) != "attachment_limit" {
		t.Fatalf("%v", err)
	}
}
