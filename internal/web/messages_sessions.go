package web

// Texte des Blocks „Entstanden in“ (IDEA-31 P4) an PR, Commit und Request. Sie
// kommen beim Start in beide Kataloge.

var messagesSessions = map[string]string{
	"sess.title":           "Built in",
	"sess.untitled":        "Session",
	"sess.person":          "Session of %s",
	"sess.person_nobody":   "Session",
	"sess.weak":            "weak",
	"sess.weak_tip":        "Assigned by time window only; the author could not be checked. A candidate, not proof.",
	"sess.more":            "+%d more",
	"sess.hidden":          "%d more sessions, not shared",
	"sess.hidden_one":      "1 more session, not shared",
	"sess.created_pr":      "opened the PR",
	"sess.merged_pr":       "merged the PR",
	"sess.n_commits":       "%d commits",
	"sess.n_commits_one":   "1 commit",
	"sess.via_pr":          "via PR #%d",
	"sess.via_patch":       "same change, other hash",
	"sess.origin_pr":       "PR #%d",
	"sess.origin_evidence": "evidence",
}

var messagesSessionsDE = map[string]string{
	"sess.title":           "Entstanden in",
	"sess.untitled":        "Session",
	"sess.person":          "Session von %s",
	"sess.person_nobody":   "Session",
	"sess.weak":            "schwach",
	"sess.weak_tip":        "Nur über das Zeitfenster zugeordnet, der Autor ließ sich nicht prüfen. Ein Kandidat, kein Beleg.",
	"sess.more":            "+%d weitere",
	"sess.hidden":          "%d weitere Sessions, nicht freigegeben",
	"sess.hidden_one":      "1 weitere Session, nicht freigegeben",
	"sess.created_pr":      "hat den PR angelegt",
	"sess.merged_pr":       "hat den PR gemergt",
	"sess.n_commits":       "%d Commits",
	"sess.n_commits_one":   "1 Commit",
	"sess.via_pr":          "über PR #%d",
	"sess.via_patch":       "gleiche Änderung, anderer Hash",
	"sess.origin_pr":       "PR #%d",
	"sess.origin_evidence": "Nachweis",
}

func init() {
	for k, v := range messagesSessions {
		messages[k] = v
	}
	for k, v := range messagesSessionsDE {
		messagesDE[k] = v
	}
}
