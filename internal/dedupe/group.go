/*
GoogleTakeoutFixer - A tool to easily clean and organize Google Photos Takeout exports
Copyright (C) 2026 feloex

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package dedupe

import "sort"

// Sensitivity is a named preset mapping to perceptual Hamming-distance cutoffs.
// Presets are preferred over a raw slider because a bare number is hard for
// users to reason about; the underlying thresholds are still adjustable.
//
// Two hashes gate a match: dHash (edge/gradient structure) AND aHash (overall
// brightness distribution) must both be within range. Requiring agreement
// curbs dHash's tendency to over-group low-detail images (skies, gradients),
// where small structural noise looks like a near-duplicate but the brightness
// profile differs.
type Sensitivity struct {
	Name        string
	Description string
	Threshold   int // max dHash Hamming distance to consider a near-duplicate
	AHashMax    int // max aHash Hamming distance allowed for the same match
}

var (
	// SensitivityIdentical matches only visually identical images (after
	// normalization) — recompressions and format changes of the same photo.
	SensitivityIdentical = Sensitivity{"Identical", "Same image, possibly recompressed or in a different format", 2, 4}
	// SensitivityNear matches near-duplicates: light brightness/color edits.
	SensitivityNear = Sensitivity{"Near-duplicate", "Minor edits, color/brightness adjustments", 6, 10}
	// SensitivityLoose matches possible edits: stronger filters. More false positives.
	SensitivityLoose = Sensitivity{"Similar / possible edits", "Filters and heavier edits; review carefully", 12, 18}
)

// Presets returns the built-in sensitivity presets, loosest last.
func Presets() []Sensitivity {
	return []Sensitivity{SensitivityIdentical, SensitivityNear, SensitivityLoose}
}

// MatchKind labels why two files were grouped.
type MatchKind int

const (
	// MatchExact: byte-identical files (SHA-256 equal). Certain duplicates.
	MatchExact MatchKind = iota
	// MatchPerceptual: visually similar within the chosen threshold.
	MatchPerceptual
)

func (k MatchKind) String() string {
	if k == MatchExact {
		return "Identical bytes"
	}
	return "Visually similar"
}

// Group is a cluster of files considered duplicates of one another.
type Group struct {
	Kind  MatchKind
	Files []PhotoInfo
	// MaxDistance is the largest pairwise dHash distance within the group
	// (0 for exact-byte groups). Lower means more confident.
	MaxDistance int
}

// FindGroups clusters photos into duplicate groups. Byte-identical files are
// grouped first as high-confidence MatchExact groups; the remaining decoded
// files are clustered by perceptual similarity within sens.Threshold.
//
// Only groups with more than one file are returned. Exact groups are listed
// before perceptual ones, and within each kind tighter groups come first.
func FindGroups(photos []PhotoInfo, sens Sensitivity) []Group {
	var groups []Group

	// 1) Exact-byte grouping.
	byBytes := make(map[FileHash][]PhotoInfo)
	for _, p := range photos {
		byBytes[p.FileHash] = append(byBytes[p.FileHash], p)
	}
	exactMembers := make(map[string]bool) // paths already claimed by an exact group
	for _, members := range byBytes {
		if len(members) < 2 {
			continue
		}
		sortByPath(members)
		groups = append(groups, Group{Kind: MatchExact, Files: members, MaxDistance: 0})
		for _, m := range members {
			exactMembers[m.Path] = true
		}
	}

	// 2) Perceptual grouping over decoded files not already in an exact group.
	//    Union-find by dHash distance threshold.
	var candidates []PhotoInfo
	for _, p := range photos {
		if p.Decoded && !exactMembers[p.Path] {
			candidates = append(candidates, p)
		}
	}

	uf := newUnionFind(len(candidates))
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			dDist := candidates[i].DHash.HammingDistance(candidates[j].DHash)
			aDist := candidates[i].AHash.HammingDistance(candidates[j].AHash)
			// Both fingerprints must agree: structure (dHash) and brightness
			// profile (aHash). This rejects low-detail false positives that
			// pass dHash alone.
			if dDist <= sens.Threshold && aDist <= sens.AHashMax {
				uf.union(i, j)
			}
		}
	}

	clusters := make(map[int][]int)
	for i := range candidates {
		root := uf.find(i)
		clusters[root] = append(clusters[root], i)
	}

	for _, idxs := range clusters {
		if len(idxs) < 2 {
			continue
		}
		members := make([]PhotoInfo, 0, len(idxs))
		for _, i := range idxs {
			members = append(members, candidates[i])
		}
		maxDist := 0
		for a := 0; a < len(members); a++ {
			for b := a + 1; b < len(members); b++ {
				if d := members[a].DHash.HammingDistance(members[b].DHash); d > maxDist {
					maxDist = d
				}
			}
		}
		sortByPath(members)
		groups = append(groups, Group{Kind: MatchPerceptual, Files: members, MaxDistance: maxDist})
	}

	// Order: exact first, then perceptual; tighter (lower distance) first.
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Kind != groups[j].Kind {
			return groups[i].Kind < groups[j].Kind
		}
		return groups[i].MaxDistance < groups[j].MaxDistance
	})
	return groups
}

func sortByPath(p []PhotoInfo) {
	sort.Slice(p, func(i, j int) bool { return p[i].Path < p[j].Path })
}

// KeepCandidate returns the member of an exact-byte group to keep when
// auto-collapsing duplicates: the one with the shortest path, ties broken
// alphabetically. Shortest path favors the canonical "Photos from <year>"
// copy over a nested album copy. The remaining members are the ones to trash.
//
// It panics on an empty group — callers only invoke it for groups of 2+.
func KeepCandidate(g Group) (keep PhotoInfo, trash []PhotoInfo) {
	best := 0
	for i := 1; i < len(g.Files); i++ {
		if preferKeep(g.Files[i].Path, g.Files[best].Path) {
			best = i
		}
	}
	keep = g.Files[best]
	for i, f := range g.Files {
		if i != best {
			trash = append(trash, f)
		}
	}
	return keep, trash
}

// preferKeep reports whether path a is a better keep than path b: shorter
// first, then alphabetically lower as a deterministic tiebreak.
func preferKeep(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// unionFind is a minimal disjoint-set structure for clustering.
type unionFind struct {
	parent []int
	rank   []int
}

func newUnionFind(n int) *unionFind {
	uf := &unionFind{parent: make([]int, n), rank: make([]int, n)}
	for i := range uf.parent {
		uf.parent[i] = i
	}
	return uf
}

func (uf *unionFind) find(x int) int {
	for uf.parent[x] != x {
		uf.parent[x] = uf.parent[uf.parent[x]] // path halving
		x = uf.parent[x]
	}
	return x
}

func (uf *unionFind) union(a, b int) {
	ra, rb := uf.find(a), uf.find(b)
	if ra == rb {
		return
	}
	if uf.rank[ra] < uf.rank[rb] {
		ra, rb = rb, ra
	}
	uf.parent[rb] = ra
	if uf.rank[ra] == uf.rank[rb] {
		uf.rank[ra]++
	}
}
