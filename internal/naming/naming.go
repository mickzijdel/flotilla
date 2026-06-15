package naming

import "fmt"

// Words is a curated word list for instance names (nautical/exploration themed).
var Words = []string{
	"atlas", "beacon", "compass", "delta", "echo", "fathom", "galley", "harbor",
	"isle", "jetty", "keel", "lagoon", "mast", "nadir", "ozone", "prow",
	"quay", "reef", "sextant", "tide", "umbra", "vector", "wake", "yardarm", "zephyr",
}

// Pick returns a free name, preferring a word whose first letter is not yet used.
// taken maps already-used names to true. When every word is taken it appends a
// numeric suffix until a free name is found.
func Pick(taken map[string]bool) string {
	usedInitials := map[byte]bool{}
	for name := range taken {
		if len(name) > 0 {
			usedInitials[name[0]] = true
		}
	}
	// First pass: free word with an unused initial.
	for _, w := range Words {
		if !taken[w] && !usedInitials[w[0]] {
			return w
		}
	}
	// Second pass: any free word.
	for _, w := range Words {
		if !taken[w] {
			return w
		}
	}
	// Fallback: suffix the first word until free.
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", Words[0], i)
		if !taken[cand] {
			return cand
		}
	}
}
