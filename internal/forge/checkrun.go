package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Check-Run „Lastenheft“ (REQ-601 AC-3): ein Prüfstand am PR, den ghosttree schreibt.
// Das braucht das Recht „Checks: write“, das die App nicht von selbst hat; ohne es
// antwortet GitHub dem Token-Antrag mit 422 oder 403, und PublishCheckRun gibt
// ErrPermissionMissing zurück. Der Aufrufer lässt dann alles bleiben.

// Ergebnis eines Check-Runs.
const (
	CheckSuccess = "success"
	CheckFailure = "failure"
	CheckNeutral = "neutral"
)

// CheckRun ist, was ghosttree an einen Commit schreibt. Name und Titel sind kurz,
// Summary ist Markdown (GitHub nimmt bis zu 65.535 Zeichen).
type CheckRun struct {
	Name, Title, Summary string
	// Conclusion ist CheckSuccess, CheckFailure oder CheckNeutral.
	Conclusion string
	// DetailsURL ist die Seite in ghosttree, auf die „Details“ führt (leer: keine).
	DetailsURL string
}

const maxCheckSummary = 60000

// PublishCheckRun legt am Commit headSHA des Repos einen abgeschlossenen Check-Run an.
// GitHub zeigt je Name den jüngsten; ein neuer Aufruf ersetzt darum, was zuvor
// dastand. Fehlt der App das Recht „Checks: write“, kommt ErrPermissionMissing.
func (g *GitHubApp) PublishCheckRun(ctx context.Context, installationID int64, repo, headSHA string, run CheckRun) error {
	parts, ok := repoParts(repo)
	if !ok || !hexSHA(headSHA) {
		return fmt.Errorf("check run: repo must look like github.com/owner/repo and the sha must be hexadecimal")
	}
	switch run.Conclusion {
	case CheckSuccess, CheckFailure, CheckNeutral:
	default:
		return fmt.Errorf("check run: conclusion must be success, failure or neutral")
	}
	tok, err := g.installationToken(ctx, installationID, map[string]string{"checks": "write"})
	if err != nil {
		return tokenRefusal(err)
	}
	summary := run.Summary
	if r := []rune(summary); len(r) > maxCheckSummary {
		summary = string(r[:maxCheckSummary]) + "…"
	}
	payload := map[string]any{
		"name": run.Name, "head_sha": headSHA, "status": "completed", "conclusion": run.Conclusion,
		"output": map[string]string{"title": run.Title, "summary": summary},
	}
	if run.DetailsURL != "" {
		payload["details_url"] = run.DetailsURL
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.cfg.APIBase+"/repos/"+url.PathEscape(parts[1])+"/"+url.PathEscape(parts[2])+"/check-runs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	var created struct {
		ID int64 `json:"id"`
	}
	if err := g.do(req, &created); err != nil {
		// Ein Token ohne das Recht wird vom Check-Run-Aufruf mit 403 abgewiesen, wenn
		// die Installation es nicht freigegeben hat.
		return commitRefusal(err)
	}
	return nil
}
