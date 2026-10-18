package identity

import (
	"strings"
	"unicode"
)

// Whole-word policy, not substring matching: e.g. Scunthorpe, Dickson and
// Assassin remain valid. The list is intentionally reviewable and lives only
// in core. It is an English baseline, not a claim to cover every language.
var profaneHandleWords = strings.Fields(`fuck fucker fuckers fucking motherfucker motherfuckers fuckyou fuckoff shit shitty shithead bullshit asshole assholes arsehole bastard bastards bitch bitches bitchass cunt cunts cocksucker dickhead dickheads pussy pussies nigger niggers nigga niggas faggot faggots fag kike kikes spic spics chink chinks trannie tranny retard retarded rape rapist rapists heilhitler hitler nazi nazis`)

var handleLeet = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t", "8", "b", "9", "g", "v", "u")

func profaneHandle(handle string) bool {
	// Common camouflage: mixed case, leetspeak, and numeric prefixes/suffixes.
	// Check both literal digits and their common substitutions.
	words := []string{handle, strings.TrimFunc(handle, unicode.IsDigit)}
	start := 0
	for i := 1; i < len(handle); i++ {
		// Also recognize clear camel-case compounds, e.g. BadWordPlayer, without
		// treating arbitrary substrings of ordinary names as independent words.
		if handle[i] >= 'A' && handle[i] <= 'Z' && handle[i-1] >= 'a' && handle[i-1] <= 'z' {
			words = append(words, handle[start:i])
			start = i
		}
	}
	words = append(words, handle[start:])
	for _, word := range words {
		lower := strings.ToLower(word)
		normalized := handleLeet.Replace(strings.TrimFunc(lower, unicode.IsDigit))
		for _, candidate := range []string{lower, normalized, collapseHandleLetters(normalized)} {
			candidates := []string{candidate}
			for _, prefix := range []string{"official", "player", "gamer", "user", "real", "the", "its", "pro", "xx", "x"} {
				if strings.HasPrefix(candidate, prefix) {
					candidates = append(candidates, strings.TrimPrefix(candidate, prefix))
				}
			}
			for _, base := range candidates {
				forms := []string{base}
				for _, suffix := range []string{"official", "player", "gamer", "user", "test", "king", "queen", "xx", "x"} {
					if strings.HasSuffix(base, suffix) {
						forms = append(forms, strings.TrimSuffix(base, suffix))
					}
				}
				for _, form := range forms {
					for _, blocked := range profaneHandleWords {
						if collapseHandleLetters(form) == collapseHandleLetters(blocked) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func collapseHandleLetters(s string) string {
	var result strings.Builder
	var last rune
	for _, r := range s {
		if r != last {
			result.WriteRune(r)
		}
		last = r
	}
	return result.String()
}
