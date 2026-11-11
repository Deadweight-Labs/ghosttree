package server

import (
	"strings"
	"unicode"
)

// asksAboutEarlierWork says whether the prompt asks about work done before: "what
// did we decide last week", "wie hatten wir das damals gelöst", "did we try this
// already", "was ist mit hlpvpngw". The hint of relevance delivery (the line "X
// also came up in session #n") searches the sessions, the largest index there is,
// and it has a use only when the prompt wants to know what was said or done
// earlier. A prompt that merely contains a rare word is not such a question.
//
// The cues are phrases and not single everyday words: "wohin" and "hattest"
// (PAIN-70) say nothing about earlier work, and neither do "before" or "bereits"
// in the middle of a sentence. A prompt that asks without one of these phrases
// loses the hint, never more: context_search finds the same session when asked.
//
// It runs on every prompt, between pressing enter and the model reading it, so it
// reads words and compares them with lists instead of running a pattern, and it
// looks only at the first and the last words of a long prompt (a pasted log is not
// a question).
func asksAboutEarlierWork(prompt string) bool {
	words := cueWords(prompt)
	for i, w := range words {
		if earlierSingles[w] {
			return true
		}
		for _, p := range earlierPhrases {
			if hasPhrase(words[i:], p) {
				return true
			}
		}
		switch {
		case earlierTimeAdjectives[w] && i+1 < len(words) && earlierTimeNouns[words[i+1]]:
			// "letzte Woche", "previous session"
			return true
		case w == "vor" && i+2 < len(words) && (earlierUnits[words[i+2]] || i+3 < len(words) && words[i+2] == "paar" && earlierUnits[words[i+3]]):
			// "vor zwei Tagen", "vor 3 Wochen", "vor ein paar Tagen"
			return true
		case (w == "schon" || w == "schonmal" || w == "bereits") && withinAfter(words, i, 5, earlierParticiples):
			// "schon mal gebaut", "schon einmal besprochen"
			return true
		case earlierPronouns[w] && i+1 < len(words) && earlierAuxiliaries[words[i+1]] && withinAfter(words, i+1, 6, earlierParticiples):
			// "wir haben das doch besprochen"
			return true
		case earlierQuestionWords[w] && i+2 < len(words) && earlierAuxiliaries[words[i+1]] && earlierPronouns[words[i+2]]:
			// "wann hatten wir", "wo haben wir"
			return true
		}
	}
	return false
}

const (
	// cueHead and cueTail are how many words at the start and at the end of a
	// prompt are read.
	cueHead = 60
	cueTail = 20
)

// cueWords are the words of the prompt, lower case, cut at anything that is not a
// letter or a digit; of a long prompt the first cueHead and the last cueTail.
func cueWords(prompt string) []string {
	words := strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) > cueHead+cueTail {
		words = append(words[:cueHead:cueHead], words[len(words)-cueTail:]...)
	}
	return words
}

// hasPhrase says whether words start with the words of phrase.
func hasPhrase(words []string, phrase []string) bool {
	if len(words) < len(phrase) {
		return false
	}
	for i, p := range phrase {
		if words[i] != p {
			return false
		}
	}
	return true
}

// withinAfter says whether one of the n words after words[i] is in set. The word
// right after "schon" counts like any other: "schon besprochen" is as much a
// question about earlier work as "schon mal besprochen" ("mal" and "einmal" are not
// in the set, so they need no skipping).
func withinAfter(words []string, i, n int, set map[string]bool) bool {
	for j := i + 1; j <= i+n && j < len(words); j++ {
		if set[words[j]] {
			return true
		}
	}
	return false
}

func wordSetOf(list string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(list) {
		m[w] = true
	}
	return m
}

func phrasesOf(lines ...string) [][]string {
	var out [][]string
	for _, l := range lines {
		out = append(out, strings.Fields(l))
	}
	return out
}

var (
	// A time that lies behind us, in one word.
	earlierSingles = wordSetOf(`früher vorhin damals neulich zuletzt vorgestern gestern bisher seinerzeit
		earlier previously yesterday`)
	// "letzte Woche", "andere Session", "previous session": an adjective for the
	// past and a noun for a stretch of work or time.
	earlierTimeAdjectives = wordSetOf(`letzte letzten letzter letztes letztem vorige vorigen vorherige vorherigen
		vergangene vergangenen frühere früheren früherer andere anderen alte alten
		last previous past earlier other old`)
	earlierTimeNouns = wordSetOf(`mal woche monat session sitzung runde sprint konversation unterhaltung
		time week month conversation run work`)
	earlierUnits = wordSetOf(`tag tagen woche wochen monat monaten stunde stunden minuten`)
	// What was done: "gebaut", "besprochen".
	earlierParticiples = wordSetOf(`gemacht gebaut gelöst besprochen entschieden probiert versucht gesehen gehabt
		erwähnt gesagt beschlossen diskutiert geschrieben angelegt notiert festgehalten`)
	earlierPronouns      = wordSetOf(`wir ich du`)
	earlierAuxiliaries   = wordSetOf(`haben hatten hab habe hast hatte hattest`)
	earlierQuestionWords = wordSetOf(`wann wo wie was warum wieso weshalb wer`)

	earlierPhrases = phrasesOf(
		"erinnerst du dich", "weißt du noch", "kannst du dich erinnern", "wie war das noch", "wie war das damals",
		// A question about a thing the prompt names: "was ist mit hlpvpngw". Asking
		// what something is asks what was said about it before.
		"was ist", "was war", "was sind", "was waren", "wer ist", "wer war", "kennst du",
		"weißt du etwas", "weißt du was", "weißt du mehr", "erzähl mir was zu", "erzähl mir zu", "erzähl mir über",
		"what is", "what was", "what are", "what were", "who is", "who was", "tell me about", "do you know",
		"other day", "a while ago", "back then", "remember when", "do you remember",
		"did we", "have we", "had we",
		"we discussed", "we decided", "we tried", "we built", "we talked about", "we agreed", "we said", "we solved", "we fixed",
	)
)
