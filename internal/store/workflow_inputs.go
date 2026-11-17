package store

import (
	"encoding/json"
	"strings"
)

// What the machine puts together for a run about a merged pull request (REQ-696,
// stage E3). The server holds no checkout: it knows the pull request's text, its
// commits with their messages and changed files (GitHub reports them), the paths a
// client reported, the entries anchored to the pull request, and the sessions
// linked to its commits. A diff comes from GitHub where the server can ask for it
// (workflow.DefResolver.PRDiff); without it the commits stand in.

// WorkflowPRCommit is one commit of a pull request as a run reads it.
type WorkflowPRCommit struct {
	SHA, Title, Message string
	// Files are the changed paths GitHub reported for the commit, if any.
	Files []string
}

// WorkflowPR is a merged pull request as a run reads it.
type WorkflowPR struct {
	Number int64
	// Known: the server recorded the pull request (git_pull_requests), which
	// it does only for one of the project's own repository, never for a fork.
	Known                   bool
	Title, Body, Base, Head string
	Commits                 []WorkflowPRCommit
	// Paths are the changed paths a client reported for the pull request.
	Paths []string
	// Knowledge are the entries written on its commits.
	Knowledge []PRKnowledge
	// Sessions are the sessions linked to its commits by more than a time window.
	// The caller decides which of them the run may read.
	Sessions []int64
}

// WorkflowPRInput reads a pull request for a run. An unknown pull request is an
// empty one with its number: the run then has the event's data only.
func (s *Store) WorkflowPRInput(project string, pr int64) (WorkflowPR, error) {
	if s.reader != nil {
		return s.reader.WorkflowPRInput(project, pr)
	}
	project = scopeNormalize(project)
	out := WorkflowPR{Number: pr}
	brief, err := s.PullRequestBriefOf(project, pr)
	if err != nil {
		return out, err
	}
	out.Known, out.Title, out.Base, out.Head, out.Knowledge = brief.Known, brief.Title, brief.Base, brief.Head, brief.Knowledge
	_ = s.db.QueryRow(`SELECT body FROM git_pull_requests WHERE project=? AND number=?`, project, pr).Scan(&out.Body)
	rows, err := s.db.Query(`SELECT c.sha, c.title, COALESCE(m.message,''), COALESCE(m.files,'[]') FROM git_pr_commits c
		LEFT JOIN git_commit_meta m ON m.project=c.project AND m.sha=c.sha
		WHERE c.project=? AND c.pr=? AND c.in_pr=1 ORDER BY c.position, c.committed_at`, project, pr)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c WorkflowPRCommit
		var files string
		if err := rows.Scan(&c.SHA, &c.Title, &c.Message, &files); err != nil {
			rows.Close()
			return out, err
		}
		c.Files = commitFilePaths(files)
		out.Commits = append(out.Commits, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if out.Paths, err = s.pullPaths(project, pr); err != nil {
		return out, err
	}
	links, err := s.SessionsForPull(project, pr)
	if err != nil {
		return out, err
	}
	for _, l := range links {
		if !l.Weak {
			out.Sessions = append(out.Sessions, l.SessionID)
		}
	}
	return out, nil
}

// commitFilePaths reads the paths of git_commit_meta.files, which holds either
// paths or objects with a path.
func commitFilePaths(raw string) []string {
	var objs []struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(raw), &objs) == nil {
		var out []string
		for _, o := range objs {
			if p := strings.TrimSpace(o.Path); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	var paths []string
	_ = json.Unmarshal([]byte(raw), &paths)
	return paths
}

// RequestTitle is an issue by its number and title.
type RequestTitle struct {
	ID    int64
	Title string
}

// OpenRequestTitles lists the open issues of a project, newest first, at most
// limit: what a run may point to instead of proposing a second issue for the
// same wish. It reads no rights; the caller drops what the run's audience may
// not read, so it asks for more than it shows.
func (s *Store) OpenRequestTitles(project string, limit int) ([]RequestTitle, error) {
	if s.reader != nil {
		return s.reader.OpenRequestTitles(project, limit)
	}
	rows, err := s.db.Query(`SELECT id, title FROM requests WHERE project=? AND state='open' ORDER BY id DESC LIMIT ?`, scopeNormalize(project), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestTitle
	for rows.Next() {
		var r RequestTitle
		if err := rows.Scan(&r.ID, &r.Title); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FeatureProjectOf is the project of an active feature, for the check of a
// proposed relation; an unknown or retired feature has none.
func (s *Store) FeatureProjectOf(id int64) (string, bool) {
	if s.reader != nil {
		return s.reader.FeatureProjectOf(id)
	}
	f, err := s.featureByID(id)
	if err != nil || f.State != "active" {
		return "", false
	}
	return f.Project, true
}

// ProjectInstallation is the GitHub App installation a project's repository
// belongs to; false without one.
func (s *Store) ProjectInstallation(project string) (int64, bool) {
	if s.reader != nil {
		return s.reader.ProjectInstallation(project)
	}
	targets, err := s.PullSyncTargets(project)
	if err != nil || len(targets) == 0 || targets[0].InstallationID <= 0 {
		return 0, false
	}
	return targets[0].InstallationID, true
}
