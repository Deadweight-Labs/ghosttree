package web

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
)

var (
	reportBlockRE = regexp.MustCompile(`(?s)<div class="treport" id="report">.*?</details></div>`)
	noteFormRE    = regexp.MustCompile(`(?s)<form[^>]*data-draft.*?</form>`)
	tokenRE       = regexp.MustCompile(`name="csrf_token" value="[^"]*"`)
)

// REQ-685 AC-2: the note typed into the field of the task page goes along with "Report anyway" and with the
// plain report (static/ui-parts.js copies it into the form of the report). The server writes it as the note of
// the report, so nothing that was typed is lost. The fixture is what the real page renders for the forms the
// script works on.
func TestReportCarriesTheNoteThatWasTyped(t *testing.T) {
	e, task, _ := taskFixture(t)
	path := "/ui/x/shell/tasks/" + itoa(task.ID)
	_, page := fetchPage(t, e.Member, e.Base+path)
	report, note := reportBlockRE.FindString(page), noteFormRE.FindString(page)
	if report == "" || note == "" {
		t.Fatalf("the report forms or the note field are not on the page:\n%.800s", page)
	}
	if !strings.Contains(report, `name="confirm_open" value="1"`) || !strings.Contains(note, `name="note"`) {
		t.Fatalf("the forms are not the ones the script expects:\n%s\n%s", report, note)
	}
	goldenFixture(t, "task-report.html", []byte(tokenRE.ReplaceAllString(note+"\n"+report, `name="csrf_token" value="tok"`)+"\n"))

	// What the script sends: the form of "Report anyway" with the note in it.
	resp := tpPost(t, e, e.Member, "/ui/tasks/done", url.Values{"id": {itoa(task.ID)}, "back": {"task"}, "confirm_open": {"1"}, "note": {"The key is in place, rights 0600."}})
	resp.Body.Close()
	got, err := e.St.TaskByID(task.ID)
	if err != nil || got.State != humantask.Done || got.DoneNote != "The key is in place, rights 0600." {
		t.Fatalf("a report with a note: %+v err=%v", got, err)
	}
	_, done := fetchPage(t, e.Member, e.Base+path)
	if !strings.Contains(done, "The key is in place, rights 0600.") {
		t.Error("the page of the reported task does not show the note that came with the report")
	}
}
