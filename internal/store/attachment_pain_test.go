package store

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/idea"
	"github.com/Deadweight-Labs/ghosttree/internal/pain"
)

// REQ-671: a pain and an issue carry images that belong to the object itself, with the
// rights of working on it; a viewer who may not read the object gets the answer of a
// number that does not exist.
func TestImagesOnAPainAndOnAnIssueBelongToTheObjectAndFollowItsRights(t *testing.T) {
	w := newPainWorld(t)
	p := w.newPain(t, "mia").Pain
	rid := w.request(t, "Mit Screenshot")
	data := testPNG(t, 6)

	for _, c := range []struct {
		name, subject, owner string
		id                   int64
		missing              int64
		code                 string
	}{
		{"pain", idea.SubjectPain, idea.OwnerPain, p.ID, p.ID + 500, "pain_not_found"},
		{"issue", idea.SubjectRequest, idea.OwnerRequest, rid, rid + 500, "request_not_found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			meta, err := w.pa["mia"].Attach(AttachInput{Subject: c.subject, SubjectID: c.id, Owner: c.owner, Data: data, Alt: "Screenshot", Actor: w.actor("mia")})
			if err != nil || meta.Subject != c.subject || meta.Owner != c.owner || meta.OwnerRef != "" || meta.Width != 6 || meta.Alt != "Screenshot" {
				t.Fatalf("%+v %v", meta, err)
			}
			// The same image in the same place again is no second attachment.
			if again, err := w.pa["mia"].Attach(AttachInput{Subject: c.subject, SubjectID: c.id, Owner: c.owner, Data: data, Actor: w.actor("mia")}); err != nil || again.ID != meta.ID {
				t.Fatalf("%+v %v", again, err)
			}
			// A guest reads the list and the bytes, but attaches nothing.
			if list, err := w.pa["gus"].AttachmentsOf(c.subject, c.id); err != nil || len(list) != 1 {
				t.Fatalf("a guest lists %+v: %v", list, err)
			}
			if _, got, err := w.pa["gus"].AttachmentData(meta.ID); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("a guest reads the image: %v", err)
			}
			if w.pa["gus"].CanAttach(c.subject, c.id) || !w.pa["mia"].CanAttach(c.subject, c.id) {
				t.Error("CanAttach does not follow the right to work on the object")
			}
			if _, err := w.pa["gus"].Attach(AttachInput{Subject: c.subject, SubjectID: c.id, Owner: c.owner, Data: testPNG(t, 7), Actor: w.actor("gus")}); !errors.Is(err, ErrAccessForbidden) {
				t.Fatalf("a guest attaches: %v", err)
			}
			// A stranger gets the answer of a number that does not exist, in every call.
			_, errList := w.pa["nora"].AttachmentsOf(c.subject, c.id)
			_, errListMissing := w.pa["nora"].AttachmentsOf(c.subject, c.missing)
			_, errAttach := w.pa["nora"].Attach(AttachInput{Subject: c.subject, SubjectID: c.id, Owner: c.owner, Data: data, Actor: w.actor("nora")})
			_, _, errData := w.pa["nora"].AttachmentData(meta.ID)
			_, _, errDataMissing := w.pa["nora"].AttachmentData(meta.ID + 50)
			if ruleCode(errList) != c.code || ruleCode(errListMissing) != c.code || ruleCode(errAttach) != c.code {
				t.Fatalf("a stranger: list %v, missing %v, attach %v", errList, errListMissing, errAttach)
			}
			if ruleCode(errData) != "attachment_not_found" || ruleCode(errDataMissing) != "attachment_not_found" {
				t.Fatalf("a stranger reads the image: %v / %v", errData, errDataMissing)
			}
			if w.pa["nora"].CanAttach(c.subject, c.id) {
				t.Error("a stranger may attach")
			}
			// Whoever may edit the object takes the image off again; a member who did not write it may not.
			if err := w.pa["rex"].RemoveAttachment(meta.ID); !errors.Is(err, ErrAccessForbidden) {
				t.Fatalf("%v", err)
			}
			if err := w.pa["lena"].RemoveAttachment(meta.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.pa["gus"].AttachmentData(meta.ID); ruleCode(err) != "attachment_not_found" {
				t.Fatalf("%v", err)
			}
		})
	}
}

// The owner is the object itself: a pain has no variants, and an owner of another kind
// than the subject is refused.
func TestImageOwnersMustFitTheSubject(t *testing.T) {
	w := newPainWorld(t)
	p := w.newPain(t, "mia").Pain
	rid := w.request(t, "Owner")
	i := w.workshopIdea(t, "Idee")
	data := testPNG(t, 4)
	for name, in := range map[string]AttachInput{
		"pain with owner idea":        {Subject: idea.SubjectPain, SubjectID: p.ID, Owner: idea.OwnerIdea},
		"pain with owner request":     {Subject: idea.SubjectPain, SubjectID: p.ID, Owner: idea.OwnerRequest},
		"pain with a variant":         {Subject: idea.SubjectPain, SubjectID: p.ID, Owner: idea.OwnerVariant, VariantID: 1},
		"pain with a reply":           {Subject: idea.SubjectPain, SubjectID: p.ID, Owner: idea.OwnerReply, ThreadID: 1},
		"issue with owner pain":       {Subject: idea.SubjectRequest, SubjectID: rid, Owner: idea.OwnerPain},
		"idea with owner request":     {Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerRequest},
		"idea with owner pain":        {Subject: idea.SubjectIdea, SubjectID: i.ID, Owner: idea.OwnerPain},
		"issue with owner idea (old)": {Subject: idea.SubjectRequest, SubjectID: rid, Owner: idea.OwnerIdea},
	} {
		in.Data, in.Actor = data, w.actor("mia")
		if _, err := w.pa["mia"].Attach(in); ruleCode(err) != "owner_invalid" {
			t.Errorf("%s: %v, want owner_invalid", name, err)
		}
	}
	var n int
	w.st.DB().QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d refused images were stored", n)
	}
}

// A pain held back (planted before its project existed) answers as an unknown pain to
// everyone but its author: the list, the upload and the image itself.
func TestImagesOfAHeldPainAnswerLikeAnUnknownOne(t *testing.T) {
	st := orgStore(t, "robin", "anna", "carl")
	st.SetAccessMode(AccessMode{Enforce: true})
	o := mustOrg(t, st, "person:1", "Alpha", "alpha")
	code, _, _ := st.CreateInvitation("person:1", o.ID, "", OrgMember, 0)
	if _, err := st.AcceptInvitation("person:2", code); err != nil {
		t.Fatal(err)
	}
	const remote = "github.com/alpha/app"
	carl := st.Access(Principal{ID: "person:3", Label: "carl"})
	pd, err := carl.CreatePain(CreatePainInput{Project: remote, Title: "planted pain", Actor: pain.Actor{AccountID: 3, Person: "carl"}, AuthorID: 3})
	if err != nil {
		t.Fatal(err)
	}
	data := testPNG(t, 5)
	meta, err := carl.Attach(AttachInput{Subject: idea.SubjectPain, SubjectID: pd.Pain.ID, Owner: idea.OwnerPain, Data: data, Actor: idea.Actor{Person: "carl", AccountID: 3}})
	if err != nil {
		t.Fatalf("the author attaches to the own pain: %v", err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, SourceGitHub, "alpha", 9, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimPrefix("person:1", o.ID, "github.com/alpha/*", VisibilityShared); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureProject("person:2", remote); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]Principal{"anna": {ID: "person:2", Label: "anna"}, "robin": {ID: "person:1", Label: "robin"}} {
		pa := st.Access(p)
		_, errList := pa.AttachmentsOf(idea.SubjectPain, pd.Pain.ID)
		_, errUnknown := pa.AttachmentsOf(idea.SubjectPain, pd.Pain.ID+900)
		_, errAttach := pa.Attach(AttachInput{Subject: idea.SubjectPain, SubjectID: pd.Pain.ID, Owner: idea.OwnerPain, Data: testPNG(t, 9), Actor: idea.Actor{Person: name}})
		_, _, errData := pa.AttachmentData(meta.ID)
		if ruleCode(errList) != "pain_not_found" || ruleCode(errUnknown) != "pain_not_found" || ruleCode(errAttach) != "pain_not_found" || ruleCode(errData) != "attachment_not_found" {
			t.Errorf("%s: list %v / unknown %v / attach %v / data %v", name, errList, errUnknown, errAttach, errData)
		}
		if pa.CanAttach(idea.SubjectPain, pd.Pain.ID) {
			t.Errorf("%s may attach to a held pain", name)
		}
	}
	if _, _, err := carl.AttachmentData(meta.ID); err != nil {
		t.Errorf("the author lost the own image: %v", err)
	}
}

// The table of an older database does not know the subject pain and the owners request
// and pain: it is rebuilt once, every row stays with its number and its bytes.
const oldAttachmentsDDL = `
CREATE TABLE attachments(
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL CHECK(project != ''),
  subject_kind TEXT NOT NULL CHECK(subject_kind IN ('idea','request')),
  subject_id INTEGER NOT NULL,
  owner_kind TEXT NOT NULL CHECK(owner_kind IN ('idea','reply','variant')),
  owner_ref TEXT NOT NULL DEFAULT '',
  mime TEXT NOT NULL CHECK(mime IN ('image/png','image/jpeg','image/webp')),
  bytes INTEGER NOT NULL CHECK(bytes > 0 AND bytes <= 5242880),
  width INTEGER NOT NULL, height INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  alt TEXT NOT NULL DEFAULT '',
  person TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  data BLOB NOT NULL);
CREATE INDEX attachments_subject ON attachments(subject_kind, subject_id);
`

func TestAttachmentsTableOfAnOlderDatabaseIsRebuiltForPainsAndKeepsItsRows(t *testing.T) {
	st := accessFixture(t)
	db := st.DB()
	if _, err := db.Exec(`DROP TABLE attachments`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(oldAttachmentsDDL); err != nil {
		t.Fatal(err)
	}
	old := testPNG(t, 3)
	rows := []struct {
		id                  int64
		subject, owner, ref string
	}{{7, "idea", "idea", ""}, {9, "idea", "variant", "4"}, {12, "request", "variant", "5"}}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO attachments(id,project,subject_kind,subject_id,owner_kind,owner_ref,mime,bytes,width,height,sha256,alt,person,created_at,data) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.id, roleProject, r.subject, 3, r.owner, r.ref, "image/png", len(old), 3, 3, "sum"+r.owner, "alt "+r.owner, "mia", "2026-11-01T10:00:00Z", old); err != nil {
			t.Fatal(err)
		}
	}
	// The old table refuses a pain: this is what the rebuild is for.
	if _, err := db.Exec(`INSERT INTO attachments(project,subject_kind,subject_id,owner_kind,mime,bytes,width,height,sha256,created_at,data) VALUES(?,'pain',1,'pain','image/png',1,1,1,'x','t',x'00')`, roleProject); err == nil {
		t.Fatal("the old table accepts a pain")
	}
	if err := ensureIdeaWorkshop(db); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&n)
	if n != len(rows) {
		t.Fatalf("%d rows after the rebuild, want %d", n, len(rows))
	}
	for _, r := range rows {
		var owner, ref, alt, person, created string
		var data []byte
		if err := db.QueryRow(`SELECT owner_kind,owner_ref,alt,person,created_at,data FROM attachments WHERE id=? AND subject_kind=?`, r.id, r.subject).Scan(&owner, &ref, &alt, &person, &created, &data); err != nil {
			t.Fatalf("row %d is gone: %v", r.id, err)
		}
		if owner != r.owner || ref != r.ref || alt != "alt "+r.owner || person != "mia" || created != "2026-11-01T10:00:00Z" || !bytes.Equal(data, old) {
			t.Errorf("row %d changed: %s %s %s %s %s", r.id, owner, ref, alt, person, created)
		}
	}
	// The table now takes the pain and the owners request and pain; the checks stay.
	for _, q := range []string{
		`INSERT INTO attachments(project,subject_kind,subject_id,owner_kind,mime,bytes,width,height,sha256,created_at,data) VALUES('p','pain',1,'pain','image/png',1,1,1,'a','t',x'00')`,
		`INSERT INTO attachments(project,subject_kind,subject_id,owner_kind,mime,bytes,width,height,sha256,created_at,data) VALUES('p','request',1,'request','image/png',1,1,1,'b','t',x'00')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO attachments(project,subject_kind,subject_id,owner_kind,mime,bytes,width,height,sha256,created_at,data) VALUES('p','task',1,'pain','image/png',1,1,1,'c','t',x'00')`); err == nil {
		t.Error("an unknown subject is accepted")
	}
	// New numbers go on after the old ones, the index is back, and a second run changes nothing.
	var next int64
	db.QueryRow(`SELECT MAX(id) FROM attachments`).Scan(&next)
	if next <= 12 {
		t.Errorf("the numbering restarted: %d", next)
	}
	var idx int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='attachments_subject'`).Scan(&idx)
	var leftover int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='attachments_old'`).Scan(&leftover)
	if idx != 1 || leftover != 0 {
		t.Errorf("index %d, leftover tables %d", idx, leftover)
	}
	if err := ensureIdeaWorkshop(db); err != nil {
		t.Fatal(err)
	}
	var after int
	db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&after)
	if after != n+2 {
		t.Errorf("a second run changed the rows: %d", after)
	}
}

// An image on an issue goes along when the issue is moved to another project.
func TestImagesOnAMovedIssueFollowItToTheNewProject(t *testing.T) {
	w := newPainWorld(t)
	rid := w.request(t, "wandert mit Bild")
	if _, err := w.pa["mia"].Attach(AttachInput{Subject: idea.SubjectRequest, SubjectID: rid, Owner: idea.OwnerRequest, Data: testPNG(t, 3), Actor: w.actor("mia")}); err != nil {
		t.Fatal(err)
	}
	if err := w.st.MoveRequest(rid, "github.com/other/place", "mia", "falsches Projekt"); err != nil {
		t.Fatal(err)
	}
	var project string
	w.st.DB().QueryRow(`SELECT project FROM attachments WHERE subject_kind='request' AND subject_id=?`, rid).Scan(&project)
	if project != "github.com/other/place" {
		t.Errorf("the image stayed in %q", project)
	}
}
