package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
	"github.com/Deadweight-Labs/ghosttree/internal/humantask"
	requestdomain "github.com/Deadweight-Labs/ghosttree/internal/request"
	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// A demo commit hash from a counter: forty hex characters, always the same.
func sha(n int) string {
	return fmt.Sprintf("%07x%033x", 0xa1b2c3d+n*0x1357, uint64(n)*0x9e3779b97f4a7c15)
}

// seedPulls adds what the pages of pull requests and commits show: pull requests
// in every state with authors, packages and their progress, commits of the pull
// requests with their authors, knowledge written at commits and a task that
// waits for a person. The times of the pull requests and entries are set back
// after the store is closed (the returned function), so the lists show a few
// days instead of "now".
func seedPulls(st *store.Store, project string) func(path string) {
	p, known := st.ProjectByRemote(project)
	if !known {
		return nil
	}
	_, err := st.ConnectSourceWithInstallation("person:1", p.OrgID, "github", "robin-e2e", 77, nil)
	must(err)
	robin := st.Access(store.Principal{ID: "person:1", Label: "Robin", TokenKind: store.WebSessionKind})
	anchor := func(n int, branch string) *gitanchor.Anchor { return &gitanchor.Anchor{Commit: sha(n), Branch: branch} }

	// What was written at commits, before the pull requests exist: opening one
	// tags the anchors of its branch.
	type note struct {
		typ, title string
		n          int
		branch     string
		age        time.Duration
	}
	notes := []note{
		{"pitfall", "Check-Run schlägt bei Fork-PRs ohne Schreibrecht fehl", 3, "feat/599-pulls", 3 * time.Hour},
		{"decision", "Closed heißt geschlossen ohne Merge, damit sich die Reiter nicht überschneiden", 2, "feat/599-pulls", 20 * time.Hour},
		{"note", "Pull Requests ohne Beschreibung vom Server zeigen nur, was GitHub meldet", 3, "feat/599-pulls", 5 * time.Hour},
		{"pitfall", "Der Webhook holt Commits mit acht Sekunden Zeitgrenze", 11, "feat/inbox-gardener", 30 * time.Hour},
		{"decision", "Gärtner-Hinweise haben einen eigenen Filter im Posteingang", 12, "feat/inbox-gardener", 28 * time.Hour},
		{"note", "Releases laufen auf build-1", 21, "release/0.14.0", 2 * time.Hour},
		{"note", "Der Posteingang zählt nur offene Zeilen", 31, "main", 50 * time.Hour},
	}
	var backdate []func(db *sql.DB)
	for _, k := range notes {
		_, err := st.InsertKnowledge(store.Knowledge{Type: k.typ, Title: k.title, Body: "Aus der Arbeit am Commit.", Scope: scope.Axes{Project: project, Branch: k.branch}, Person: "Robin",
			Confidence: "trusted", GitAnchor: anchor(k.n, k.branch)})
		must(err)
		k := k
		backdate = append(backdate, func(db *sql.DB) {
			ts := time.Now().UTC().Add(-k.age).Format(time.RFC3339)
			_, err := db.Exec(`UPDATE knowledge SET created_at=?, updated_at=?, observed_at=? WHERE title=?`, ts, ts, ts, k.title)
			must(err)
		})
	}

	// A work package for the open pull request of this work, with progress and one open task.
	req := func(title string, criteria ...string) requestdomain.Detail {
		d, err := st.CreateRequest(requestdomain.CreateInput{Request: requestdomain.Request{Type: "feature", Title: title, Scope: scope.Axes{Project: project}, Person: "Robin"}, Criteria: criteria})
		must(err)
		return d
	}
	tabs := req("Pull Requests und Commits als Reiter", "Liste mit Zustandsreitern und Filtern", "Seite eines PRs mit Checkliste", "Commit-Seite zeigt, was dazu gesagt wurde", "Zahl der Commits auf der Code-Seite")
	anchors := req("Git-Anker an Einträgen", "Wissen kennt den Commit", "Request kennt Branch und PR", "Anker aus dem Hook ohne Netz nachtragen")
	inbox := req("Antwortoptionen im Posteingang", "Optionen an einer Agentenfrage", "Schnellantwort legt eine Nachricht an")
	proof := func(n int) requestdomain.Evidence {
		return requestdomain.Evidence{Kind: "commit", Ref: sha(n), Person: "Robin"}
	}
	must(st.SetCriterionState(tabs.Criteria[0].ID, "met", proof(1)))
	must(st.SetCriterionState(tabs.Criteria[1].ID, "met", proof(2)))
	must(st.SetCriterionState(anchors.Criteria[0].ID, "met", proof(3)))
	must(st.SetCriterionState(anchors.Criteria[1].ID, "met", requestdomain.Evidence{Kind: "test", Ref: "TestAnchorBranch", Person: "claude"}))
	must(st.SetCriterionState(anchors.Criteria[2].ID, "met", requestdomain.Evidence{Kind: "decision", Ref: "kept offline", Person: "Robin"}))
	must(st.SetCriterionState(inbox.Criteria[0].ID, "met", proof(11)))
	must(st.SetCriterionState(inbox.Criteria[1].ID, "met", proof(12)))
	_, err = st.CreatePackage(requestdomain.PackageInput{Project: project, Branch: "feat/599-pulls", Name: "Oberfläche nach GitHub", PR: 184, Person: "Robin",
		Members: []requestdomain.PackageMember{{RequestID: tabs.Request.ID, Role: requestdomain.RolePflicht}, {RequestID: anchors.Request.ID, Role: requestdomain.RolePflicht}, {RequestID: inbox.Request.ID, Role: requestdomain.RoleOptional}}})
	must(err)
	_, err = robin.CreateHumanTask(humantask.CreateInput{Project: project, RequestID: tabs.Request.ID, Kind: "action", Title: "Reiter Pull Requests am echten Repo ansehen", Assignee: "Lena Berger", CheckHint: "Die Liste zeigt die PRs des Repos"},
		humantask.Actor{AccountID: 1, Person: "Robin"})
	must(err)
	done := req("Gärtner-Hinweise im Posteingang", "Eigener Filter", "Hinweis erscheint nicht unter Fragen")
	must(st.SetCriterionState(done.Criteria[0].ID, "met", proof(12)))
	must(st.SetCriterionState(done.Criteria[1].ID, "met", proof(11)))
	_, err = st.CreatePackage(requestdomain.PackageInput{Project: project, Branch: "feat/inbox-gardener-2", Name: "Gärtner im Posteingang II", PR: 180, Person: "Robin",
		Members: []requestdomain.PackageMember{{RequestID: done.Request.ID, Role: requestdomain.RolePflicht}}})
	must(err)

	// The pull requests.
	type pull struct {
		n                         int64
		title, head, base, author string
		state                     string
		age                       time.Duration
	}
	pulls := []pull{
		{175, "docs: Entwurf Arbeitspakete (REQ-601)", "docs/packages", "feat/git-native", "Robin", "closed", 9 * 24 * time.Hour},
		{178, "fix(collector): Session dem Projekt zuordnen, sobald das Transkript das Arbeitsverzeichnis nennt", "fix/collector-project", "feat/git-native", "codex", "merged", 4 * 24 * time.Hour},
		{179, "feat: git-native ghosttree, Teil 5: Features, Maschinen von Orgs, Karten und Anker", "feat/git-native-5", "feat/git-native", "claude", "merged", 3 * 24 * time.Hour},
		{180, "feat: git-native ghosttree, Teil 6: Projekt und Maschine an einem Eintrag, Kommentare", "feat/git-native-6", "feat/git-native", "Robin", "merged", 2 * 24 * time.Hour},
		{181, "feat(inbox): Gärtner-Hinweise im Posteingang (REQ-613)", "feat/inbox-gardener", "dev", "claude", "open", 30 * time.Hour},
		{183, "Release 0.14.0", "release/0.14.0", "main", "codex", "open", 5 * time.Hour},
		{184, "feat(web): Pull Requests und Commits als Reiter (REQ-599)", "feat/599-pulls", "feat/git-native-599", "Robin", "open", 3 * time.Hour},
	}
	// The text of two of them, the way GitHub sends it (Markdown, and markup that must stay text).
	texts := map[int64]string{
		184: "## Was ändert sich\n\nPull Requests und Commits stehen als Reiter am Repo, mit **allem**, was dazu gesagt wurde.\n\n- Liste mit Filterfeld und Vorschlägen\n- Seite mit Unterhaltung, Checkliste, Wissen und Commits\n\nGetestet mit `go test -p 1 ./internal/web/`.\n\n<script>alert(1)</script>",
		181: "Gärtner-Hinweise erscheinen im Posteingang als eine gebündelte Zeile.",
	}
	for _, pr := range pulls {
		var body *string
		if t, ok := texts[pr.n]; ok {
			body = &t
		}
		_, err := st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: project, HeadRemote: project, Number: pr.n, Action: "opened", Title: pr.title,
			Head: pr.head, Base: pr.base, State: "open", Author: pr.author, Body: body})
		must(err)
		if pr.state != "open" {
			_, err = st.RecordPullRequest(store.PullRequestChange{InstallationID: 77, Remote: project, HeadRemote: project, Number: pr.n, Action: "closed", Title: pr.title,
				Head: pr.head, Base: pr.base, State: pr.state, Author: pr.author})
			must(err)
		}
		pr := pr
		backdate = append(backdate, func(db *sql.DB) {
			opened := time.Now().UTC().Add(-pr.age).Format(time.RFC3339)
			updated := time.Now().UTC().Add(-pr.age / 2).Format(time.RFC3339)
			merged := ""
			if pr.state == "merged" {
				merged = updated
			}
			_, err := db.Exec(`UPDATE git_pull_requests SET opened_at=?, updated_at=?, merged_at=? WHERE project=? AND number=?`, opened, updated, merged, project, pr.n)
			must(err)
		})
	}

	// The commits the pull requests carry.
	at := func(back time.Duration) string { return time.Now().UTC().Add(-back).Format(time.RFC3339) }
	commits := func(n int64, cs ...store.PullCommit) {
		res, err := st.RecordPullCommits(store.PullCommitsChange{InstallationID: 77, Remote: project, Number: n, Commits: cs})
		must(err)
		if !res.Applied {
			fmt.Fprintln(os.Stderr, "seed: commits of", n, "not applied")
		}
	}
	commits(184,
		store.PullCommit{SHA: sha(1), Author: "Robin", Title: "feat(web): Liste der Pull Requests mit Zustandsreitern (REQ-599)", CommittedAt: at(9 * time.Hour)},
		store.PullCommit{SHA: sha(2), Author: "claude", Title: "feat(web): Seite eines PRs mit Unterhaltung und Checkliste (REQ-599)", CommittedAt: at(7 * time.Hour)},
		store.PullCommit{SHA: sha(3), Author: "claude", Title: "feat(web): Reiter Commits mit Tagesgruppen (REQ-599)", CommittedAt: at(6 * time.Hour)},
		store.PullCommit{SHA: sha(4), Author: "codex", Title: "test(web): Rechte der PR- und Commit-Seiten (REQ-599)", CommittedAt: at(4 * time.Hour)})
	commits(183,
		store.PullCommit{SHA: sha(21), Author: "codex", Title: "chore(release): Version 0.14.0", CommittedAt: at(5 * time.Hour)},
		store.PullCommit{SHA: sha(22), Author: "codex", Title: "docs(changelog): 0.14.0", CommittedAt: at(5 * time.Hour)})
	commits(181,
		store.PullCommit{SHA: sha(11), Author: "claude", Title: "feat(inbox): Antwortoptionen an Agentenfragen", CommittedAt: at(32 * time.Hour)},
		store.PullCommit{SHA: sha(12), Author: "claude", Title: "feat(inbox): Schnellantwort im Posteingang", CommittedAt: at(31 * time.Hour)},
		store.PullCommit{SHA: sha(13), Author: "Robin", Title: "fix(inbox): Gärtner-Hinweise nicht unter Fragen zählen", CommittedAt: at(30 * time.Hour)})
	commits(180,
		store.PullCommit{SHA: sha(31), Author: "Robin", Title: "merge: dev (Teil 5) in feat/git-native", CommittedAt: at(50 * time.Hour)},
		store.PullCommit{SHA: sha(32), Author: "claude", Title: "feat(web): Kommentare an Issues, Ideen und Pains", CommittedAt: at(49 * time.Hour)})
	commits(179,
		store.PullCommit{SHA: sha(41), Author: "claude", Title: "feat(features): Feature-Beschreibungen mit Versionen", CommittedAt: at(74 * time.Hour)})

	return func(path string) {
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
		must(err)
		defer db.Close()
		for _, f := range backdate {
			f(db)
		}
	}
}
