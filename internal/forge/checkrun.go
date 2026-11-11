package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// PublishCheckRun schreibt den Check-Run am Commit headSHA des Repos und gibt dessen
// Nummer zurück. Ist existingID gesetzt (der Run, den ein früherer Aufruf an
// demselben Commit angelegt hat), wird er aktualisiert (PATCH), statt einen weiteren
// anzulegen: sonst wüchse die Liste der Prüfungen am Commit mit jeder Änderung. Kennt
// GitHub ihn nicht mehr, wird ein neuer angelegt. Fehlt der App das Recht „Checks:
// write“, kommt ErrPermissionMissing; kennt GitHub den Commit nicht, ErrCommitUnavailable.
func (g *GitHubApp) PublishCheckRun(ctx context.Context, installationID int64, repo, headSHA string, run CheckRun, existingID int64) (int64, error) {
	parts, ok := repoParts(repo)
	if !ok || !hexSHA(headSHA) {
		return 0, fmt.Errorf("check run: repo must look like github.com/owner/repo and the sha must be hexadecimal")
	}
	switch run.Conclusion {
	case CheckSuccess, CheckFailure, CheckNeutral:
	default:
		return 0, fmt.Errorf("check run: conclusion must be success, failure or neutral")
	}
	tok, err := g.installationToken(ctx, installationID, map[string]string{"checks": "write"})
	if err != nil {
		return 0, tokenRefusal(err)
	}
	summary := run.Summary
	if r := []rune(summary); len(r) > maxCheckSummary {
		summary = string(r[:maxCheckSummary]) + "…"
	}
	payload := map[string]any{
		"name": run.Name, "status": "completed", "conclusion": run.Conclusion,
		"output": map[string]string{"title": run.Title, "summary": summary},
	}
	if run.DetailsURL != "" {
		payload["details_url"] = run.DetailsURL
	}
	base := g.cfg.APIBase + "/repos/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(parts[2]) + "/check-runs"
	send := func(method, target string, body map[string]any) (int64, error) {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(raw))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")
		var out struct {
			ID int64 `json:"id"`
		}
		err = g.do(req, &out)
		return out.ID, err
	}
	if existingID > 0 {
		id, err := send(http.MethodPatch, base+"/"+fmt.Sprint(existingID), payload)
		if err == nil {
			if id == 0 {
				id = existingID
			}
			return id, nil
		}
		// Nur ein Run, den GitHub nicht mehr kennt (404), wird neu angelegt; jeder
		// andere Fehler bleibt einer.
		var se *statusError
		if !errors.As(err, &se) || se.code != http.StatusNotFound {
			return 0, commitRefusal(err)
		}
	}
	payload["head_sha"] = headSHA
	id, err := send(http.MethodPost, base, payload)
	if err != nil {
		// Ein Token ohne das Recht wird vom Check-Run-Aufruf mit 403 abgewiesen, wenn
		// die Installation es nicht freigegeben hat.
		return 0, commitRefusal(err)
	}
	return id, nil
}
