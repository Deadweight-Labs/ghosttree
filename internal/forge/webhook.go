package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// maxWebhookBody begrenzt, was der Webhook liest (eine große Installation meldet
// viele Repos auf einmal).
const maxWebhookBody = 8 << 20

// RepoSync nimmt eine Änderung der Repo-Liste einer Installation entgegen. Die
// Remotes sind normalisiert (github.com/owner/repo).
type RepoSync func(installationID int64, add, remove []string) error

// VerifySignature prüft den Header X-Hub-Signature-256 ("sha256=<hex>") gegen den
// Körper mit einem Vergleich in konstanter Zeit.
func VerifySignature(secret string, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok || secret == "" {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// WebhookHandler ist der Webhook der GitHub-App. Er antwortet nur auf
// Anfragen mit gültiger Signatur (sonst 401, ohne Inhalt), verarbeitet die
// Ereignisse repository (created, deleted, renamed) und installation_repositories
// (added, removed) und beantwortet alles andere mit 204. Weder Körper noch
// Signatur gelangen in eine Antwort oder in onError.
func (g *GitHubApp) WebhookHandler(sync RepoSync, onError func(error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusRequestEntityTooLarge)
			return
		}
		if !g.WebhookConfigured() || !VerifySignature(g.cfg.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		inst, add, remove := ParseRepoEvent(r.Header.Get("X-GitHub-Event"), body)
		if inst > 0 && (len(add) > 0 || len(remove) > 0) {
			if err := sync(inst, add, remove); err != nil {
				if onError != nil {
					onError(err)
				}
				http.Error(w, "could not apply", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

type webhookRepo struct {
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func remoteOf(fullName string) string {
	fullName = strings.ToLower(strings.TrimSpace(fullName))
	if strings.Count(fullName, "/") != 1 {
		return ""
	}
	return "github.com/" + fullName
}

// ParseRepoEvent übersetzt ein Ereignis in die Änderung der Repo-Liste. Für alles,
// was nicht zur Repo-Liste gehört (und für kaputte Körper), kommt nichts zurück.
func ParseRepoEvent(event string, body []byte) (installationID int64, add, remove []string) {
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository webhookRepo   `json:"repository"`
		Added      []webhookRepo `json:"repositories_added"`
		Removed    []webhookRepo `json:"repositories_removed"`
		Changes    struct {
			Repository struct {
				Name struct {
					From string `json:"from"`
				} `json:"name"`
			} `json:"repository"`
		} `json:"changes"`
	}
	if json.Unmarshal(body, &p) != nil {
		return 0, nil, nil
	}
	appendRemote := func(list []string, fullName string) []string {
		if r := remoteOf(fullName); r != "" {
			return append(list, r)
		}
		return list
	}
	switch event {
	case "repository":
		switch p.Action {
		case "created":
			add = appendRemote(add, p.Repository.FullName)
		case "deleted":
			remove = appendRemote(remove, p.Repository.FullName)
		case "renamed":
			if from := p.Changes.Repository.Name.From; from != "" && p.Repository.Owner.Login != "" {
				remove = appendRemote(remove, p.Repository.Owner.Login+"/"+from)
			}
			add = appendRemote(add, p.Repository.FullName)
		}
	case "installation_repositories":
		switch p.Action {
		case "added":
			for _, r := range p.Added {
				add = appendRemote(add, r.FullName)
			}
		case "removed":
			for _, r := range p.Removed {
				remove = appendRemote(remove, r.FullName)
			}
		}
	}
	return p.Installation.ID, add, remove
}
