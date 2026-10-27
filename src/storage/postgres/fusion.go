package postgres

import (
	"cmp"
	"slices"
)

// rrfK is the RRF dampening constant, 60, from Cormack et al. It is what makes
// the fusion consensus-driven rather than winner-take-all: rank 1 beats rank 3
// in one list by 1/61 - 1/63 ≈ 0.0005, while a second list naming an id at all
// adds at least 1/(60 + n). A smaller k sharpens the head of each list until the
// fusion is decided by whichever mode happened to rank first.
const rrfK = 60.0

// RRF fuses ranked id lists by Reciprocal Rank Fusion: 1/(k + rank), k = 60.
//
// By RANK and never by score: the modes return a ts_rank_cd relevance, a trigram
// fraction and a cosine distance — three units, three scales, one better when
// smaller. Only their orders are comparable.
//
// It is a UNION, not an intersection. A mode that errored contributes an empty
// list, and intersecting would turn that into an empty page — at the caller,
// indistinguishable from "nothing matched".
func RRF(ranked ...[]string) []string {
	// Sized for the worst case, every list disjoint. Fusing N modes at the
	// retrieval depth is thousands of ids; letting the map and slice grow into
	// that costs 32 allocations and 169KB against 6 and 79KB presized. Same
	// wall time either way — this is for the garbage collector, not latency.
	capacity := 0
	for _, list := range ranked {
		capacity += len(list)
	}
	scores := make(map[string]float64, capacity)
	order := make([]string, 0, capacity)

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

	// By score, then by id, so pages are stable (ADR-0017).
	slices.SortFunc(order, func(left, right string) int {
		if byScore := cmp.Compare(scores[right], scores[left]); byScore != 0 {
			return byScore
		}
		return cmp.Compare(left, right)
	})
	return order
}
