package prose

import (
	"strings"
	"unicode"
)

// Op sagt, was mit einem Satz geschehen ist.
type Op int

const (
	Same Op = iota
	Removed
	Added
)

// Change ist ein Satz und sein Schicksal.
type Change struct {
	Op   Op
	Text string
}

// Diff vergleicht zwei Prosatexte satzweise.
func Diff(oldText, newText string) []Change {
	return diffSentences(Sentences(oldText), Sentences(newText))
}

// abbrev sind Abkürzungen, deren Punkt kein Satzende ist. Einbuchstabige
// werden gesondert erkannt und stehen hier nicht.
var abbrev = map[string]bool{
	"bzw": true, "ca": true, "ggf": true, "inkl": true, "Nr": true,
	"sog": true, "usw": true, "vgl": true, "evtl": true, "ff": true,
}

// Sentences zerlegt einen Text in Sätze. Zeilenumbrüche trennen immer, denn
// sie sind in diesen Beschreibungen Absatz- oder Listengrenzen.
func Sentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, splitLine(line)...)
		}
	}
	return out
}

func splitLine(line string) []string {
	r := []rune(line)
	var out []string
	start := 0
	for i := 0; i < len(r); i++ {
		if r[i] != '.' && r[i] != '!' && r[i] != '?' {
			continue
		}
		if i+1 >= len(r) || !unicode.IsSpace(r[i+1]) {
			continue
		}
		k := i + 1
		for k < len(r) && unicode.IsSpace(r[k]) {
			k++
		}
		if k >= len(r) || !opensSentence(r[k]) || isAbbrev(r[start:i]) {
			continue
		}
		out = append(out, strings.TrimSpace(string(r[start:i+1])))
		start, i = k, k-1
	}
	if rest := strings.TrimSpace(string(r[start:])); rest != "" {
		out = append(out, rest)
	}
	return out
}

func opensSentence(c rune) bool {
	return unicode.IsUpper(c) || unicode.IsDigit(c) || strings.ContainsRune("„\"'(«[—-–", c)
}

func isAbbrev(before []rune) bool {
	word := string(before)
	if i := strings.LastIndexAny(word, " \t"); i >= 0 {
		word = word[i+1:]
	}
	return len([]rune(word)) == 1 || abbrev[word]
}

// diffSentences ist ein gewöhnlicher LCS-Vergleich. Bei ein paar Dutzend
// Sätzen je Beschreibung ist die quadratische Tabelle nicht der Rede wert.
func diffSentences(a, b []string) []Change {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	out := []Change{}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, Change{Same, a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, Change{Removed, a[i]})
			i++
		default:
			out = append(out, Change{Added, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, Change{Removed, a[i]})
	}
	for ; j < m; j++ {
		out = append(out, Change{Added, b[j]})
	}
	return out
}
