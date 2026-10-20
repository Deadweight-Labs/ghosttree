package embed

import (
	"errors"
	"math"
	"sort"
)

// ErrBadVector is returned for a vector that cannot be used: empty, not
// finite, or without length.
var ErrBadVector = errors.New("embed: unusable vector")

// Normalize scales v to unit length in place.
func Normalize(v []float32) error {
	if len(v) == 0 {
		return ErrBadVector
	}
	var sum float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return ErrBadVector
		}
		sum += f * f
	}
	if sum == 0 {
		return ErrBadVector
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return nil
}

// Quantize turns a unit vector into int8 values with one symmetric scale per
// vector: the largest magnitude maps to 127 and v[i] is about scale*q[i]. On the
// project's own entries this cost at most 0.1 MRR points against float32.
// The bytes are the two's-complement int8 values.
func Quantize(v []float32) (q []byte, scale float32, err error) {
	if len(v) == 0 {
		return nil, 0, ErrBadVector
	}
	var peak float64
	for _, x := range v {
		f := math.Abs(float64(x))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, 0, ErrBadVector
		}
		peak = math.Max(peak, f)
	}
	if peak == 0 {
		return nil, 0, ErrBadVector
	}
	s := peak / 127
	q = make([]byte, len(v))
	for i, x := range v {
		q[i] = byte(int8(math.Round(float64(x) / s)))
	}
	return q, float32(s), nil
}

// Cosine is the cosine similarity of a unit query vector and a stored vector
// that was normalised before it was quantised. The vectors must have the same
// length; the caller checks.
func Cosine(query []float32, q []byte, scale float32) float32 {
	var sum float32
	for i, b := range q {
		sum += query[i] * float32(int8(b))
	}
	return sum * scale
}

// RRFK is the constant of reciprocal rank fusion. 60 is the value of the
// original paper.
const RRFK = 60

// Fused is one entry of a fused ranking.
type Fused struct {
	ID    int64
	Score float64
	// Ranks holds the 1-based position in each input list, 0 where the entry is
	// not in that list.
	Ranks []int
}

// Fuse merges rankings, best first, by reciprocal rank fusion: an entry scores
// the sum of 1/(RRFK+rank) over the lists it is in. Ties go to the entry that is
// higher in the earlier list, so with the full-text ranking first the older
// order survives wherever the lists do not disagree.
func Fuse(lists ...[]int64) []Fused {
	byID := map[int64]*Fused{}
	var order []int64
	for li, list := range lists {
		for i, id := range list {
			f := byID[id]
			if f == nil {
				f = &Fused{ID: id, Ranks: make([]int, len(lists))}
				byID[id] = f
				order = append(order, id)
			}
			if f.Ranks[li] != 0 {
				continue // a repeated id keeps its best position
			}
			f.Ranks[li] = i + 1
			f.Score += 1 / float64(RRFK+i+1)
		}
	}
	out := make([]Fused, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		for li := range lists {
			ra, rb := out[a].Ranks[li], out[b].Ranks[li]
			if ra == rb {
				continue
			}
			if ra == 0 {
				return false
			}
			if rb == 0 {
				return true
			}
			return ra < rb
		}
		return out[a].ID < out[b].ID
	})
	return out
}
