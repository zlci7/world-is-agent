package textsearch

import (
	"strings"
	"unicode"
)

// Terms bounds input before tokenizing Latin words, digits and adjacent Han pairs.
func Terms(text string, maxChars, maxTerms int) []string {
	if maxChars <= 0 || maxTerms <= 0 {
		return nil
	}
	var terms []string
	seen := map[string]bool{}
	add := func(term string) {
		if term != "" && !seen[term] && len(terms) < maxTerms {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	var latin strings.Builder
	var previous rune
	count := 0
	flush := func() { add(latin.String()); latin.Reset() }
	for _, r := range text {
		if count >= maxChars || len(terms) >= maxTerms {
			break
		}
		count++
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			if previous != 0 {
				add(string([]rune{previous, r}))
			}
			previous = r
		case unicode.Is(unicode.Latin, r) || unicode.IsDigit(r):
			previous = 0
			latin.WriteRune(unicode.ToLower(r))
		default:
			previous = 0
			flush()
		}
	}
	flush()
	return terms
}
