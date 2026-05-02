package smplkit

import (
	"fmt"
	"regexp"
	"strings"
)

// hexRegex accepts CSS hex colors in the forms #RGB, #RRGGBB, or #RRGGBBAA.
var hexRegex = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// Color is a CSS hex color string. It is immutable: construct a fresh Color
// via NewColor or NewColorRGB to change a value. Mirrors the Python SDK's
// Color type (rule 8 of the cross-SDK overhaul).
//
// The constructors validate their inputs at the SDK boundary so customer
// mistakes surface at the call site rather than as a server 400 later.
type Color struct {
	hex string
}

// NewColor constructs a Color from a CSS hex string. Accepts #RGB, #RRGGBB,
// or #RRGGBBAA. Any other input returns a non-nil error.
func NewColor(hex string) (Color, error) {
	if !hexRegex.MatchString(hex) {
		return Color{}, fmt.Errorf("smplkit: invalid color %q: must be a CSS hex string like '#RGB', '#RRGGBB', or '#RRGGBBAA'", hex)
	}
	// normalize to lowercase so equality is canonical
	return Color{hex: strings.ToLower(hex)}, nil
}

// NewColorRGB constructs a Color from 0–255 integer components. Each
// component is validated; out-of-range values produce a non-nil error.
func NewColorRGB(r, g, b int) (Color, error) {
	for name, v := range map[string]int{"r": r, "g": g, "b": b} {
		if v < 0 || v > 255 {
			return Color{}, fmt.Errorf("smplkit: Color.RGB %s must be in range 0–255, got %d", name, v)
		}
	}
	return Color{hex: fmt.Sprintf("#%02x%02x%02x", r, g, b)}, nil
}

// MustColor is the panic-on-error variant of NewColor for compile-time
// constants. Customer code that builds colors from runtime input should
// always use NewColor and check the error.
func MustColor(hex string) Color {
	c, err := NewColor(hex)
	if err != nil {
		panic(err)
	}
	return c
}

// Hex returns the canonical (lowercase) CSS hex string.
func (c Color) Hex() string { return c.hex }

// String returns the canonical hex string, satisfying fmt.Stringer.
func (c Color) String() string { return c.hex }

// IsZero reports whether c is the zero Color value (i.e. unset).
func (c Color) IsZero() bool { return c.hex == "" }
