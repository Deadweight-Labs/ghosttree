package web

// Texte des Blocks „Entstanden in“ (IDEA-31 P4) an PR, Commit und Request. Sie
// kommen beim Start in beide Kataloge.

var messagesSessions = map[string]string{
	"sessions.link.title":           "Built in",
	"sessions.link.untitled":        "Session",
	"sessions.link.person":          "Session of %s",
	"sessions.link.person_nobody":   "Session",
	"sessions.link.weak":            "weak",
	"sessions.link.weak_tip":        "Assigned by time window only; the author could not be checked. A candidate, not proof.",
	"sessions.link.more":            "+%d more",
	"sessions.link.hidden":          "%d more sessions, not shared",
	"sessions.link.hidden_one":      "1 more session, not shared",
	"sessions.link.created_pr":      "opened the PR",
	"sessions.link.merged_pr":       "merged the PR",
	"sessions.link.n_commits":       "%d commits",
	"sessions.link.n_commits_one":   "1 commit",
	"sessions.link.via_pr":          "via PR #%d",
	"sessions.link.via_patch":       "same change, other hash",
	"sessions.link.origin_pr":       "PR #%d",
	"sessions.link.origin_evidence": "evidence",
}

var messagesSessionsDE = map[string]string{
	"sessions.link.title":           "Entstanden in",
	"sessions.link.untitled":        "Session",
	"sessions.link.person":          "Session von %s",
	"sessions.link.person_nobody":   "Session",
	"sessions.link.weak":            "schwach",
	"sessions.link.weak_tip":        "Nur über das Zeitfenster zugeordnet, der Autor ließ sich nicht prüfen. Ein Kandidat, kein Beleg.",
	"sessions.link.more":            "+%d weitere",
	"sessions.link.hidden":          "%d weitere Sessions, nicht freigegeben",
	"sessions.link.hidden_one":      "1 weitere Session, nicht freigegeben",
	"sessions.link.created_pr":      "hat den PR angelegt",
	"sessions.link.merged_pr":       "hat den PR gemergt",
	"sessions.link.n_commits":       "%d Commits",
	"sessions.link.n_commits_one":   "1 Commit",
	"sessions.link.via_pr":          "über PR #%d",
	"sessions.link.via_patch":       "gleiche Änderung, anderer Hash",
	"sessions.link.origin_pr":       "PR #%d",
	"sessions.link.origin_evidence": "Nachweis",
}

func init() {
	for k, v := range messagesSessions {
		messages[k] = v
	}
	for k, v := range messagesSessionsDE {
		messagesDE[k] = v
	}
}
