package rawcodec

import (
	"encoding/binary"
	"sort"
)

// Parameter des Trainings: Länge einer gezählten Folge und eines Segments.
const (
	trainDmer = 8
	trainSeg  = 256
)

// Train baut ein Wörterbuch von höchstens size Bytes aus einer Stichprobe von
// Zeilen. Es ist eine vereinfachte Form des COVER-Verfahrens von zstd:
//
// Jede Folge von trainDmer Bytes zählt, in wie vielen Zeilen sie vorkommt
// (nicht wie oft: was sich in einer Zeile wiederholt, findet deflate schon
// selbst). Die Stichprobe wird in so viele gleich lange Abschnitte (Epochen)
// geteilt, wie Segmente in das Wörterbuch passen; aus jeder Epoche kommt das
// Segment von trainSeg Bytes, dessen noch nicht abgedeckte Folgen zusammen am
// häufigsten sind. Was ein gewähltes Segment abdeckt, zählt für die folgenden
// nicht mehr.
//
// Die Segmente stehen nach Wert geordnet, das wertvollste am Ende: deflate
// erreicht die letzten Bytes des Wörterbuchs mit den kürzesten Abständen und
// hat sie am längsten im Fenster.
//
// Das Ergebnis hängt nur von der Stichprobe und ihrer Reihenfolge ab.
func Train(samples [][]byte, size int) []byte {
	if size <= 0 || size > DictSize {
		size = DictSize
	}
	freq := map[uint64]int32{}
	seen := map[uint64]struct{}{}
	total := 0
	for _, s := range samples {
		if len(s) < trainDmer {
			continue
		}
		clear(seen)
		for i := 0; i+trainDmer <= len(s); i++ {
			k := binary.LittleEndian.Uint64(s[i:])
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				freq[k]++
			}
		}
		total += len(s)
	}
	// Folgen aus nur einer Zeile helfen keiner anderen.
	for k, n := range freq {
		if n < 2 {
			delete(freq, k)
		}
	}
	if len(freq) == 0 {
		return nil
	}

	type segment struct {
		sample, at int
		score      int64
	}
	epochs := max(size/trainSeg, 1)
	epochLen := max(total/epochs, trainSeg)
	var chosen []segment
	si, off := 0, 0 // nächste Startposition: Zeile si, Byte off
	window := map[uint64]int32{}
	for e := 0; e < epochs && si < len(samples); e++ {
		budget := epochLen // Startpositionen, die diese Epoche prüft
		best := segment{score: 0}
		for budget > 0 && si < len(samples) {
			s := samples[si]
			last := len(s) - trainDmer // letzte Startposition mit einer ganzen Folge
			if off > last {
				si, off = si+1, 0
				continue
			}
			// Fenster über die Folgen, die bei off beginnen und im Segment liegen.
			clear(window)
			var score int64
			end := min(off+trainSeg, len(s)) - trainDmer // letzte Folge im Segment
			for i := off; i <= end; i++ {
				k := binary.LittleEndian.Uint64(s[i:])
				if window[k]++; window[k] == 1 {
					score += int64(freq[k])
				}
			}
			p := off
			for {
				if score > best.score {
					best = segment{sample: si, at: p, score: score}
				}
				budget--
				if p == last || budget == 0 {
					break
				}
				// Um ein Byte weiter: die Folge bei p fällt heraus, die nach
				// dem bisherigen Ende kommt hinzu, solange die Zeile reicht.
				k := binary.LittleEndian.Uint64(s[p:])
				if window[k]--; window[k] == 0 {
					delete(window, k)
					score -= int64(freq[k])
				}
				p++
				if end+1 <= last && end+1 < p+trainSeg-trainDmer+1 {
					end++
					k := binary.LittleEndian.Uint64(s[end:])
					if window[k]++; window[k] == 1 {
						score += int64(freq[k])
					}
				}
			}
			if p >= last {
				si, off = si+1, 0
			} else {
				off = p + 1
			}
		}
		if best.score <= 0 {
			continue
		}
		chosen = append(chosen, best)
		s := samples[best.sample]
		for i := best.at; i+trainDmer <= min(best.at+trainSeg, len(s)); i++ {
			delete(freq, binary.LittleEndian.Uint64(s[i:]))
		}
	}

	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].score > chosen[j].score })
	// Von hinten füllen: das wertvollste Segment steht ganz am Ende.
	dict := make([]byte, size)
	at := size
	for _, c := range chosen {
		if at == 0 {
			break
		}
		s := samples[c.sample]
		part := s[c.at:min(c.at+trainSeg, len(s))]
		if len(part) > at {
			part = part[len(part)-at:]
		}
		at -= len(part)
		copy(dict[at:], part)
	}
	return dict[at:]
}
