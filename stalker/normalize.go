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

// orphanPunctRE matches leading or trailing punctuation characters that are
// left stranded after superscript removal (e.g. the "/" in "HD/RAW" once both
// HD and RAW have been stripped).
var orphanPunctRE = regexp.MustCompile(`^[\s/\-_,;:.#*]+|[\s/\-_,;:.#*]+$`)

// CleanGenreForM3U8 prepares a raw portal genre string for use as a
// group-title value in an M3U8 playlist. It:
//
//  1. Strips superscript/subscript decorator characters.
//  2. Splits on "|" and discards any segment that no longer contains an
//     alphanumeric character (i.e. segments whose entire content was
//     decorators), then cleans leading/trailing orphaned punctuation from
//     each remaining segment.
//  3. Rejoins surviving segments with "| " and returns the result.
//
// Examples:
//
//	"US| MAX ESPN ᴴᴰ/ᴿᴬᵂ ⁶⁰ᶠᵖˢ"  →  "US| MAX ESPN"
//	"4K| ᵁᴴᴰ ³⁸⁴⁰ᴾ"              →  "4K"
//	"US| MLB TEAM PPV"            →  "US| MLB TEAM PPV"
//	""                            →  ""
func CleanGenreForM3U8(s string) string {
	s = StripSuperscripts(s)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "|")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		// Strip orphaned punctuation from each segment.
		p := strings.TrimSpace(orphanPunctRE.ReplaceAllString(part, ""))
		// Discard the segment entirely if it has no alphanumeric content.
		hasAlnum := false
		for _, r := range p {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				hasAlnum = true
				break
			}
		}
		if hasAlnum {
			kept = append(kept, p)
		}
	}
	// Join surviving segments with a single space. Pipes are intentionally
	// dropped — external tools like Dispatcharr treat | as a hierarchy
	// delimiter in group-title values and may misparse or ignore entries
	// that contain them.
	return strings.Join(kept, " ")
}
