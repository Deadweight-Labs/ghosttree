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
	return g.WebhookHandlerWithPulls(sync, nil, onError)
}

// PullSync nimmt ein PR-Ereignis entgegen (REQ-597): die Zuordnung von Branch
// zu PR-Nummer und der Merge-Stand. Alle Textfelder sind Fremddaten.
type PullSync func(PullEvent) error

// WebhookHandlerWithPulls ist WebhookHandler, dazu pull_request (opened,
// reopened, synchronize, edited, closed) an pulls. Ohne pulls wird das Ereignis
// wie jedes unbekannte mit 204 beantwortet.
func (g *GitHubApp) WebhookHandlerWithPulls(sync RepoSync, pulls PullSync, onError func(error)) http.Handler {
	return g.WebhookHandlerWithRenames(sync, pulls, nil, onError)
}

// RepoRename nimmt die Umbenennung eines Repos entgegen (repository, action
// renamed): from und to sind normalisierte Remotes. Sie läuft vor RepoSync, damit
// das bestehende Projekt umzieht, bevor die Listenabgleichung den neuen Namen
// sieht. Ob dabei etwas umgezogen ist, ist Sache des Empfängers; RepoSync
// bekommt die Änderung in jedem Fall wie bisher als add und remove.
type RepoRename func(installationID int64, from, to string) error

// WebhookHandlerWithRenames ist WebhookHandlerWithPulls, dazu die Umbenennung an
// rename. Ohne rename bleibt es bei add und remove, wie vor dieser Funktion.
func (g *GitHubApp) WebhookHandlerWithRenames(sync RepoSync, pulls PullSync, rename RepoRename, onError func(error)) http.Handler {
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
		if ev, ok := ParsePullEvent(r.Header.Get("X-GitHub-Event"), body); ok && pulls != nil {
			if err := pulls(ev); err != nil {
				if onError != nil {
					onError(err)
				}
				http.Error(w, "could not apply", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		inst, add, remove := ParseRepoEvent(r.Header.Get("X-GitHub-Event"), body)
		if rename != nil {
			if rinst, from, to, ok := ParseRepoRename(r.Header.Get("X-GitHub-Event"), body); ok && rinst > 0 {
				if err := rename(rinst, from, to); err != nil {
					if onError != nil {
						onError(err)
					}
					http.Error(w, "could not apply", http.StatusInternalServerError)
					return
				}
			}
		}
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

// ParseRepoRename liest aus einem repository-Ereignis mit action renamed den
// alten und den neuen Namen als normalisierte Remotes. ok ist false für alles
// andere und für ein Ereignis, das den alten Namen nicht nennt.
func ParseRepoRename(event string, body []byte) (installationID int64, from, to string, ok bool) {
	if event != "repository" {
		return 0, "", "", false
	}
	var p struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository webhookRepo `json:"repository"`
		Changes    struct {
			Repository struct {
				Name struct {
					From string `json:"from"`
				} `json:"name"`
			} `json:"repository"`
		} `json:"changes"`
	}
	if json.Unmarshal(body, &p) != nil || p.Action != "renamed" {
		return 0, "", "", false
	}
	old := p.Changes.Repository.Name.From
	if old == "" || p.Repository.Owner.Login == "" {
		return 0, "", "", false
	}
	from, to = remoteOf(p.Repository.Owner.Login+"/"+old), remoteOf(p.Repository.FullName)
	if from == "" || to == "" || from == to {
		return 0, "", "", false
	}
	return p.Installation.ID, from, to, true
}

// PullEvent ist ein pull_request-Ereignis in der Form, die der Store braucht.
type PullEvent struct {
	InstallationID int64
	// Remote ist das Repo des PRs (Base), HeadRemote das des Head-Branches; bei
	// einem PR aus einem Fork sind sie verschieden.
	Remote, HeadRemote string
	Number             int64
	Action             string
	Title              string
	Head, Base         string
	DefaultBranch      string
	MergeSHA           string
	// HeadSHA ist der neueste Commit des Head-Branches; an ihn hängt der Check-Run.
	HeadSHA string
	// UpdatedAt ist updated_at des PRs im Ereignis (RFC 3339): die Zeit, auf die sich
	// HeadSHA bezieht. Webhooks kommen nicht in Reihenfolge an.
	UpdatedAt string
	// RepoPrivate ist das Feld private des Repos im Ereignis (nil, wenn es fehlt).
	RepoPrivate *bool
	// Author ist der Login des PR-Autors.
	Author string
	// State ist open, closed oder merged.
	State string
}

// ParsePullEvent übersetzt ein pull_request-Ereignis. ok ist false für alles
// andere, für Aktionen ohne Bedeutung hier und für kaputte Körper. Geprüft wird
// nur die Form; Branch-Namen und Titel bereinigt der Store.
func ParsePullEvent(event string, body []byte) (PullEvent, bool) {
	if event != "pull_request" {
		return PullEvent{}, false
	}
	var p struct {
		Action       string `json:"action"`
		Number       int64  `json:"number"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			Private       *bool  `json:"private"`
		} `json:"repository"`
		PullRequest struct {
			UpdatedAt      string `json:"updated_at"`
			Number         int64  `json:"number"`
			Title          string `json:"title"`
			State          string `json:"state"`
			Merged         bool   `json:"merged"`
			MergeCommitSHA string `json:"merge_commit_sha"`
			User           struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				Ref  string `json:"ref"`
				SHA  string `json:"sha"`
				Repo *struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(body, &p) != nil {
		return PullEvent{}, false
	}
	switch p.Action {
	case "opened", "reopened", "synchronize", "edited", "closed":
	default:
		return PullEvent{}, false
	}
	n := p.PullRequest.Number
	if n == 0 {
		n = p.Number
	}
	ev := PullEvent{
		InstallationID: p.Installation.ID, Remote: remoteOf(p.Repository.FullName), Number: n, Action: p.Action,
		Title: p.PullRequest.Title, Head: p.PullRequest.Head.Ref, Base: p.PullRequest.Base.Ref,
		DefaultBranch: p.Repository.DefaultBranch, MergeSHA: p.PullRequest.MergeCommitSHA, HeadSHA: p.PullRequest.Head.SHA, UpdatedAt: p.PullRequest.UpdatedAt, RepoPrivate: p.Repository.Private, Author: p.PullRequest.User.Login, State: "open",
	}
	if p.PullRequest.Head.Repo != nil {
		ev.HeadRemote = remoteOf(p.PullRequest.Head.Repo.FullName)
	}
	if p.Action == "closed" || p.PullRequest.State == "closed" {
		ev.State = "closed"
		if p.PullRequest.Merged {
			ev.State = "merged"
		}
	}
	return ev, true
}
