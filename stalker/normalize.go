package stalker

import (
	"regexp"
	"strings"
)

var deriveCatStripRE = regexp.MustCompile(`(?i)^\s*(\[[^\]]*\]|\([^\)]*\)|\{[^\}]*\})\s*`)

// DeriveCategory extracts a clean top-level category from a raw portal genre
// name. It mirrors the groupKey/deriveCategory logic used in the filter UI so
// that group-title values in the M3U8 output are consistent with what the
// webui shows.
//
// Examples:
//
//	"US| MAX ESPN ᴴᴰ/ᴿᴬᵂ"  => "US"
//	"4K| ᵁᴴᴰ ³⁸⁴⁰ᴾ"        => "4K"
//	"Sports"                 => "Sports"
//	""                       => "Other"
func DeriveCategory(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return "Other"
	}

	// Strip leading bracket noise like [UK], (VIP), {HD}.
	n = deriveCatStripRE.ReplaceAllString(n, "")
	n = strings.TrimSpace(n)
	n = strings.Join(strings.Fields(n), " ")

	// Normalize unicode-ish separators and bullets into spaces.
	repl := strings.NewReplacer(
		"•", " ",
		"·", " ",
		"—", "-",
		"–", "-",
		"→", " ",
		"⇒", " ",
		"»", " ",
	)
	n = repl.Replace(n)
	n = strings.Join(strings.Fields(n), " ")

	// Normalize pipe spacing so "US | foo", "US| foo", "US |foo" all behave
	// the same.
	pipeNorm := strings.ReplaceAll(n, " | ", "|")
	pipeNorm = strings.ReplaceAll(pipeNorm, "| ", "|")
	pipeNorm = strings.ReplaceAll(pipeNorm, " |", "|")
	pipeNorm = strings.ReplaceAll(pipeNorm, "||", "|")
	if i := strings.Index(pipeNorm, "|"); i > 0 {
		left := strings.TrimSpace(pipeNorm[:i])
		if left != "" {
			return left
		}
	}

	// Fallback: replace common separators with spaces and take the first token.
	// For short country-code prefixes (US, UK, MX …) include the second token
	// so "US Sports" stays "US Sports" rather than just "US".
	seps := []string{"/", ":", ">", "-", "_", "\\", ".", ","}
	for _, s := range seps {
		n = strings.ReplaceAll(n, s, " ")
	}
	n = strings.Join(strings.Fields(n), " ")
	parts := strings.Split(n, " ")
	if len(parts) == 0 {
		return "Other"
	}
	first := strings.TrimSpace(parts[0])
	if first == "" {
		return "Other"
	}
	if len(first) <= 3 && len(parts) > 1 {
		return strings.TrimSpace(first + " " + parts[1])
	}
	return first
}

// StripSuperscripts removes Unicode superscript and subscript characters that
// IPTV portals use for visual decoration (e.g. ᴴᴰ, ᴿᴬᵂ, ⁶⁰ᶠᵖˢ, ᵁᴴᴰ, ³⁸⁴⁰ᴾ).
// After removal any runs of whitespace left behind are collapsed to a single
// space and the result is trimmed.
//
// The ranges stripped are:
//
//	U+00B2–U+00B3, U+00B9          legacy superscript digits ²³¹ in Latin-1
//	U+02B0–U+02FF                   Spacing Modifier Letters (ᶠᵖˢ …)
//	U+1D00–U+1DBF                   Phonetic Extensions + Supplement (ᴴᴰᴿᴬᵂᵁ …)
//	U+2070–U+209F                   Superscripts and Subscripts block
//
// Characters outside these ranges — including all real-language scripts — are
// left untouched.
func StripSuperscripts(s string) string {
	mapped := strings.Map(func(r rune) rune {
		if (r >= 0x00B2 && r <= 0x00B3) ||
			r == 0x00B9 ||
			(r >= 0x02B0 && r <= 0x02FF) ||
			(r >= 0x1D00 && r <= 0x1DBF) ||
			(r >= 0x2070 && r <= 0x209F) {
			return -1
		}
		return r
	}, s)
	// Collapse any runs of whitespace created by the removals.
	return strings.Join(strings.Fields(mapped), " ")
}
