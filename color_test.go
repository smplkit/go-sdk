package smplkit_test

import (
	"strings"
	"testing"

	"github.com/smplkit/go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewColor_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"#fff", "#fff"},
		{"#FFF", "#fff"},
		{"#ef4444", "#ef4444"},
		{"#EF4444", "#ef4444"},
		{"#ef4444aa", "#ef4444aa"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			c, err := smplkit.NewColor(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, c.Hex())
			assert.Equal(t, tc.want, c.String())
		})
	}
}

func TestNewColor_Invalid(t *testing.T) {
	cases := []string{
		"",
		"red",
		"#",
		"#ff",
		"#fffff",
		"#ggg",
		"ef4444",      // missing #
		"#ef4444aabb", // too many digits
	}
	for _, tc := range cases {
		name := tc
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			_, err := smplkit.NewColor(tc)
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), "invalid color"))
		})
	}
}

func TestNewColorRGB_Valid(t *testing.T) {
	c, err := smplkit.NewColorRGB(239, 68, 68)
	require.NoError(t, err)
	assert.Equal(t, "#ef4444", c.Hex())

	black, err := smplkit.NewColorRGB(0, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, "#000000", black.Hex())

	white, err := smplkit.NewColorRGB(255, 255, 255)
	require.NoError(t, err)
	assert.Equal(t, "#ffffff", white.Hex())
}

func TestNewColorRGB_OutOfRange(t *testing.T) {
	cases := []struct {
		r, g, b int
	}{
		{-1, 0, 0},
		{0, -1, 0},
		{0, 0, -1},
		{256, 0, 0},
		{0, 256, 0},
		{0, 0, 256},
	}
	for _, tc := range cases {
		_, err := smplkit.NewColorRGB(tc.r, tc.g, tc.b)
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "must be in range"))
	}
}

func TestMustColor(t *testing.T) {
	c := smplkit.MustColor("#abc")
	assert.Equal(t, "#abc", c.Hex())
	assert.Panics(t, func() { _ = smplkit.MustColor("not-a-color") })
}

func TestColor_IsZero(t *testing.T) {
	var z smplkit.Color
	assert.True(t, z.IsZero())
	c, _ := smplkit.NewColor("#fff")
	assert.False(t, c.IsZero())
}

