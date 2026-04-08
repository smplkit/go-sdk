package smplkit

import (
	"strings"
	"unicode"
)

// keyToDisplayName converts a kebab-case or snake_case key to a title-case
// display name. For example: "checkout-v2" → "Checkout V2",
// "user_service" → "User Service".
func keyToDisplayName(key string) string {
	// Replace separators with spaces.
	s := strings.NewReplacer("-", " ", "_", " ").Replace(key)
	// Title-case each word.
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			runes := []rune(w)
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}
