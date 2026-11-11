package gitcmd

import "strings"

// wordKind sagt, ob die Shell den Text eines Wortes verändert, bevor git ihn sieht.
// Ein Wort in einfachen Anführungszeichen oder ohne Sonderzeichen ist wörtlich; was
// "$VAR", "$(…)" oder Backticks enthält, erst nach der Ersetzung bekannt.
type wordKind uint8

const (
	kindExpands wordKind = 1 << iota // $VAR, ${…}, Backtick in einem Wort ohne einfache Anführungszeichen
	kindSubst                        // enthält "$( … )"
)

// heredocBody ist der Rumpf eines Heredocs. literal: die Kennung stand in
// Anführungszeichen, die Shell ersetzt im Rumpf nichts.
type heredocBody struct {
	text    string
	literal bool
}

// commitMessage liest die Nachricht, die ein git commit oder git merge selbst trägt.
// Mehrere -m werden zu Absätzen. Leer, wenn der Befehl keine Nachricht trägt oder
// ihren Text nicht wörtlich nennt; es ist besser, keine zu melden als eine falsche
// (der Server hielte sie für die Nachricht eines Commits, den es gar nicht war).
// added: git hängt Zeilen an (--signoff, --trailer), nur die Titelzeile stimmt.
func commitMessage(sub string, args []string, kinds []wordKind, stdin []heredocBody) (msg string, added bool) {
	var paras []string
	giveUp, fromStdin := false, false
	// value liefert den Text eines Wertes; false, wenn die Shell ihn erst bildet.
	value := func(v string, k wordKind) (string, bool) {
		switch {
		case k == 0:
			return v, true
		case k == kindSubst:
			return substHeredocText(v)
		}
		return "", false
	}
	// next gibt das Wort hinter i mit seiner Art zurück.
	next := func(i int) (string, wordKind, bool) {
		if i+1 < len(args) {
			return args[i+1], kinds[i+1], true
		}
		return "", 0, false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		if strings.HasPrefix(a, "--") {
			name, attached, hasEq := strings.Cut(a[2:], "=")
			switch name {
			case "message":
				v, k := attached, kinds[i]
				if !hasEq {
					var ok bool
					if v, k, ok = next(i); !ok {
						giveUp = true
						continue
					}
					i++
				}
				if t, ok := value(v, k); ok {
					paras = append(paras, t)
				} else {
					giveUp = true
				}
			case "file":
				v := attached
				if !hasEq {
					v, _, _ = next(i)
					i++
				}
				if v == "-" && sub == "commit" {
					fromStdin = true
				} else {
					giveUp = true
				}
			case "signoff", "trailer":
				added = true
				if name == "trailer" && !hasEq {
					i++
				}
			case "fixup", "squash", "reuse-message", "reedit-message":
				giveUp = true
				if !hasEq {
					i++
				}
			case "author", "date", "cleanup", "template", "pathspec-from-file", "strategy", "strategy-option", "into-name":
				if !hasEq {
					i++
				}
			}
			continue
		}
		// Bündel kurzer Schalter: -qam "x", -m"x", -F -.
		for j := 1; j < len(a); j++ {
			r := a[j]
			if sub == "merge" && (r == 's' || r == 'X') {
				// Strategie des Merge: der Wert gehört dazu.
				if j == len(a)-1 {
					i++
				}
				break
			}
			if r == 's' && sub == "commit" {
				added = true
				continue
			}
			if r == 'S' || r == 'u' {
				break // -S[keyid], -u[mode]: der Rest des Wortes ist der Wert, kein Bündel
			}
			if !strings.ContainsRune("mFcCt", rune(r)) {
				continue
			}
			v, k := a[j+1:], kinds[i]
			if v == "" {
				var ok bool
				if v, k, ok = next(i); !ok {
					giveUp = true
					break
				}
				i++
			}
			switch r {
			case 'm':
				if t, ok := value(v, k); ok {
					paras = append(paras, t)
				} else {
					giveUp = true
				}
			case 'F':
				if v == "-" && sub == "commit" {
					fromStdin = true
				} else {
					giveUp = true
				}
			case 'c', 'C':
				giveUp = true
			}
			break
		}
	}
	if fromStdin {
		if len(paras) > 0 || len(stdin) == 0 || (!stdin[0].literal && hasExpansion(stdin[0].text)) {
			return "", false
		}
		return strings.TrimSpace(stdin[0].text), added
	}
	if giveUp || len(paras) == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.Join(paras, "\n\n")), added
}

// substHeredocText liest den Text aus "$(cat <<'EOF' … EOF)", der Form, in der
// Agenten eine lange Nachricht übergeben. Alles andere in einer Ersetzung ($(date),
// $(git rev-parse …)) bleibt unbekannt.
func substHeredocText(word string) (string, bool) {
	if !strings.HasPrefix(word, "$(") || !strings.HasSuffix(word, ")") {
		return "", false
	}
	inner := strings.TrimSpace(word[2 : len(word)-1])
	line, rest, ok := strings.Cut(inner, "\n")
	if !ok {
		return "", false
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "cat" {
		return "", false
	}
	op := strings.Join(fields[1:], "")
	if !strings.HasPrefix(op, "<<") {
		return "", false
	}
	op = op[2:]
	strip := strings.HasPrefix(op, "-")
	op = strings.TrimPrefix(op, "-")
	delim := strings.Trim(op, `'"\`)
	if delim == "" || strings.ContainsAny(delim, " \t'\"\\") {
		return "", false
	}
	literal := delim != op
	var body []string
	closed := false
	lines := strings.Split(rest, "\n")
	for n, l := range lines {
		cmp := strings.TrimRight(l, "\r")
		if strip {
			cmp = strings.TrimLeft(cmp, "\t")
		}
		if cmp == delim {
			// Hinter der Kennung darf nur Leerraum folgen.
			if strings.TrimSpace(strings.Join(lines[n+1:], "")) != "" {
				return "", false
			}
			closed = true
			break
		}
		if strip {
			l = strings.TrimLeft(l, "\t")
		}
		body = append(body, l)
	}
	if !closed {
		return "", false
	}
	text := strings.Join(body, "\n")
	if !literal && hasExpansion(text) {
		return "", false
	}
	return text, true
}

// hasExpansion sagt, ob ein Text, den die Shell nicht wörtlich nimmt, etwas enthält,
// das sie ersetzen würde: $name, ${…}, $(…), $1, $$ oder einen Backtick.
func hasExpansion(s string) bool {
	if strings.ContainsRune(s, '`') {
		return true
	}
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		c := s[i+1]
		if c == '_' || c == '{' || c == '(' || c == '@' || c == '*' || c == '#' || c == '?' || c == '!' || c == '$' || c == '-' ||
			c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}
