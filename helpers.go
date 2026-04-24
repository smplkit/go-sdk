package smplkit

import (
	"reflect"
	"strings"
	"unicode"
)

// mapsEqual reports whether two map[string]interface{} are deeply equal.
// Returns true if both are nil or both have identical structure.
func mapsEqual(a, b map[string]interface{}) bool {
	return reflect.DeepEqual(a, b)
}

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
