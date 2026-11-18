package main

import (
	"fmt"
	"io"
	"strings"
)

// diffContext is how many unchanged lines stand around a change.
const diffContext = 2

// diffLines splits a text into lines the way a person counts them: a line end
// of Windows (\r\n) is a line end, not part of the line, and the last line end
// does not open an empty line.
func diffLines(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

type diffOp struct {
	op   byte // ' ', '-', '+'
	text string
}

// lineDiff is the edit script between two lists of lines: equal lines at the
// start and the end are skipped, the part between is matched by its longest
// common subsequence.
func lineDiff(a, b []string) []diffOp {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	var ops []diffOp
	for _, t := range a[:p] {
		ops = append(ops, diffOp{' ', t})
	}
	am, bm := a[p:len(a)-s], b[p:len(b)-s]
	// lcs[i][j] is the length of the common subsequence of am[i:] and bm[j:].
	lcs := make([][]int, len(am)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(bm)+1)
	}
	for i := len(am) - 1; i >= 0; i-- {
		for j := len(bm) - 1; j >= 0; j-- {
			if am[i] == bm[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(am) || j < len(bm) {
		switch {
		case i < len(am) && j < len(bm) && am[i] == bm[j]:
			ops = append(ops, diffOp{' ', am[i]})
			i, j = i+1, j+1
		case i < len(am) && (j == len(bm) || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, diffOp{'-', am[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', bm[j]})
			j++
		}
	}
	for _, t := range a[len(a)-s:] {
		ops = append(ops, diffOp{' ', t})
	}
	return ops
}

// printLineDiff writes what changed between two texts: the lines removed and
// added, with a few unchanged lines around them and a gap marker where more
// unchanged lines were left out. Only the lines that changed are marked.
func printLineDiff(stdout io.Writer, oldLabel, newLabel, oldText, newText string) {
	fmt.Fprintf(stdout, "--- %s\n+++ %s\n", oldLabel, newLabel)
	ops := lineDiff(diffLines(oldText), diffLines(newText))
	show := make([]bool, len(ops))
	for i, o := range ops {
		if o.op == ' ' {
			continue
		}
		for k := max(0, i-diffContext); k <= min(len(ops)-1, i+diffContext); k++ {
			show[k] = true
		}
	}
	gap := false
	for i, o := range ops {
		if !show[i] {
			gap = true
			continue
		}
		if gap {
			fmt.Fprintln(stdout, "...")
			gap = false
		}
		fmt.Fprintf(stdout, "%c%s\n", o.op, o.text)
	}
}
