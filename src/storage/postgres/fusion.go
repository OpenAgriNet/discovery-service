package postgres

import (
	"cmp"
	"slices"
)

// rrfK is the RRF dampening constant, 60, from Cormack et al.
//
// It is what makes the fusion consensus-driven rather than winner-take-all:
// the difference between rank 1 and rank 3 in one list is 1/61 - 1/63, about
// 0.0005, while a second list naming an id AT ALL adds at least 1/(60 + n).
// So an id two modes agree on beats an id one mode was certain about, which is
// the whole reason a fusion is used instead of the first mode's order. A
// smaller k sharpens the head of each list until the fusion is decided by
// whichever mode happened to rank first.
const rrfK = 60.0

// RRF fuses ranked id lists by Reciprocal Rank Fusion: 1/(k + rank), k = 60.
//
// By RANK and never by score. The three modes return a `ts_rank_cd` relevance,
// a trigram fraction and a cosine distance — three quantities in three units,
// on three scales, one of which is better when smaller. There is no scaling
// that makes them comparable and no constant that would stay right as the
// corpus grows. Their ORDERS are comparable, and that is all this reads.
//
// It is a UNION, not an intersection. A mode that errored contributes an empty
// list, and an implementation that intersected would answer such a request with
// an empty page — indistinguishable, at the caller, from "nothing matched".
func RRF(ranked ...[]string) []string {
	scores := make(map[string]float64)
	order := make([]string, 0)

	for _, list := range ranked {
		for index, id := range list {
			if _, seen := scores[id]; !seen {
				order = append(order, id)
			}
			// index + 1: the first element is rank ONE. Rank zero would make
			// the head of every list worth 1/60 regardless of k's dampening.
			scores[id] += 1.0 / (rrfK + float64(index+1))
		}
	}

	// Sorted by score, then by id. The id tiebreak is not cosmetic: the page is
	// a SLICE of this list, so two equally-scored ids that swap between two
	// runs of the same query put one of them on both page 1 and page 2 and the
	// other on neither, with nothing in the response to say so. Ties are the
	// common case, not the rare one — every id only one mode returned, at the
	// same rank, scores identically.
	slices.SortFunc(order, func(left, right string) int {
		if byScore := cmp.Compare(scores[right], scores[left]); byScore != 0 {
			return byScore
		}
		return cmp.Compare(left, right)
	})
	return order
}
