package discovery

import "strings"

// hintScore ranks how well a device's advertised name answers the user's name
// hint: 3 for the same name (case-folded), 2 when the device name sits inside
// the hint (hint "LP10 · Living" names the device "Living"), 1 when the hint
// sits inside the device name (hint "liv" for "Living"), 0 for no relation. An
// empty name never matches — strings.Contains(hint, "") is always true. mDNS
// and LSSDP discovery pick by the same score (longest name on a tie), so
// "Living" can no longer beat "Living Room" for the hint "Living Room", and
// the two paths no longer read the hint in opposite directions.
func hintScore(name, hint string) int {
	n := strings.ToLower(strings.TrimSpace(name))
	h := strings.ToLower(strings.TrimSpace(hint))
	switch {
	case n == "" || h == "":
		return 0
	case n == h:
		return 3
	case strings.Contains(h, n):
		return 2
	case strings.Contains(n, h):
		return 1
	}
	return 0
}

// hintExact reports the one match worth ending a discovery window early on:
// the device that carries the hint as its name. A partial match waits out the
// window, so a better-named device that answers later still wins.
func hintExact(name, hint string) bool { return hintScore(name, hint) == 3 }

// bestHinted returns the index of the best-scoring name among n candidates
// (longest name breaks a tie), or -1 when none relates to the hint.
func bestHinted(n int, name func(int) string, hint string) int {
	best, bestScore, bestLen := -1, 0, 0
	for i := range n {
		s := hintScore(name(i), hint)
		if s > bestScore || (s > 0 && s == bestScore && len(name(i)) > bestLen) {
			best, bestScore, bestLen = i, s, len(name(i))
		}
	}
	return best
}
