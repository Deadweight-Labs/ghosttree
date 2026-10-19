package store

import (
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
)

// RelevanceProbe is one archived prompt run through the relevance rule.
type RelevanceProbe struct {
	Project string
	Prompt  string
	Titles  []string
	// Terms[i] is the word that brought Titles[i] up.
	Terms []string
	// Hints are the very rare words of the prompt that no entry answers. They
	// become a pointer only if a request or a session the reader may see names
	// them, which the replay does not decide.
	Hints []string
}

// ProbeRelevance replays real prompts from the archive against the rule that
// decides what to deliver unasked.
//
// The threshold this measures cannot be argued from first principles, because
// what counts as a distinctive word is a property of one archive at one time.
// It also cannot be measured on invented prompts: the failure worth catching is
// the rule firing on ordinary work, and only real prompts are ordinary. The
// archive already holds every prompt ever typed at this tool, so the sample is
// free and honest, and it has to be re-run as the corpus grows.
func (s *Store) ProbeRelevance(sample, limit int) ([]RelevanceProbe, error) {
	if s.reader != nil {
		return s.reader.ProbeRelevance(sample, limit)
	}
	if sample <= 0 {
		sample = 200
	}
	// A prompt that opens with a tag is the harness talking to itself — a
	// task notification, a plugin list, the echo of a slash command. None of it
	// is typed by anyone, and none of it reaches the hook this rule runs in.
	// Leaving it in the sample would measure the rule against text it will never
	// see, and it is long and full of unusual words, so it would measure high.
	const eligible = `FROM session_chunks c JOIN sessions se ON se.id = c.session_id
		WHERE c.role = 'user' AND se.project != ''
		  AND length(c.text) BETWEEN 12 AND 4000 AND c.text NOT LIKE '<%'`
	var eligibleCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) ` + eligible).Scan(&eligibleCount); err != nil {
		return nil, err
	}
	if eligibleCount == 0 {
		return nil, nil
	}
	// Every nth prompt rather than the newest ones: a window of recent prompts
	// is one conversation about one subject, which is exactly the sample that
	// would flatter the rule.
	step := max(1, eligibleCount/sample)
	rows, err := s.db.Query(`SELECT se.project, se.account_id, c.text `+eligible+`
		AND c.rowid % ? = 0 LIMIT ?`, step, sample)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var probes []RelevanceProbe
	var accounts []int64
	for rows.Next() {
		var p RelevanceProbe
		var account int64
		if err := rows.Scan(&p.Project, &account, &p.Prompt); err != nil {
			return nil, err
		}
		probes = append(probes, p)
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	owner := instanceOwnerID(s.db)
	for i := range probes {
		// The probe must not move the counters it may later be judged by. It
		// has no session, so every prompt is judged as the first of its own:
		// the question is what the rule would say, not what a session had
		// already been told. The language gate measures the account that typed
		// the prompt, as it does in service, so legacy rows without an account
		// belong to the instance owner.
		account := accounts[i]
		if account == 0 {
			account = owner
		}
		res, err := s.relevance(RelevanceQuery{Text: probes[i].Prompt, Axes: scope.Axes{Project: probes[i].Project},
			Limit: limit, Account: fmt.Sprintf("person:%d", account)})
		if err != nil {
			return nil, err
		}
		for _, h := range res.Hits {
			probes[i].Titles = append(probes[i].Titles, h.Title)
			probes[i].Terms = append(probes[i].Terms, h.Term)
		}
		for _, h := range res.Hints {
			if len(h.Known) == 0 {
				probes[i].Hints = append(probes[i].Hints, h.Term)
			}
		}
	}
	return probes, nil
}
