package stalker

import (
	"strings"
	"unicode"
)

// CompareNatural compares two strings for stable UI/playlist ordering.
// Numbers inside names sort numerically (e.g. "CH 2" before "CH 10").
func CompareNatural(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return 0
	}
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ca, cb := rune(a[ai]), rune(b[bi])
		if unicode.IsDigit(ca) && unicode.IsDigit(cb) {
			na, nb := 0, 0
			for ai < len(a) && unicode.IsDigit(rune(a[ai])) {
				na = na*10 + int(a[ai]-'0')
				ai++
			}
			for bi < len(b) && unicode.IsDigit(rune(b[bi])) {
				nb = nb*10 + int(b[bi]-'0')
				bi++
			}
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
			continue
		}
		la := strings.ToLower(string(ca))
		lb := strings.ToLower(string(cb))
		if la != lb {
			if la < lb {
				return -1
			}
			return 1
		}
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		ai++
		bi++
	}
	if len(a) == len(b) {
		return 0
	}
	if len(a) < len(b) {
		return -1
	}
	return 1
}

// CategorySortKey returns a sort key for filter categories ("Other" sorts last).
func CategorySortKey(category string) string {
	c := strings.TrimSpace(category)
	if c == "" || strings.EqualFold(c, "other") {
		return "\xffother"
	}
	return strings.ToLower(c)
}

// GenreSortKey sorts genres within a category (by display name, natural).
func GenreSortKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
