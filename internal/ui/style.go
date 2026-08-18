// Package ui renders the agent's event stream as a live activity view.
//
// Everything here writes to stderr. stdout carries only the answer, so the
// activity view can be as decorative as it likes without ever corrupting a
// pipeline.
package ui

import (
	"os"
	"strings"
)

// Palette holds the escape sequences, or empty strings when color is off.
type Palette struct {
	Reset, Bold, Dim                string
	Red, Green, Yellow, Blue        string
	Magenta, Cyan, Gray, BrightCyan string
}

// NewPalette exposes the palette to callers outside this package.
func NewPalette(enabled bool) Palette { return newPalette(enabled) }

func newPalette(enabled bool) Palette {
	if !enabled {
		return Palette{}
	}
	return Palette{
		Reset: "\x1b[0m", Bold: "\x1b[1m", Dim: "\x1b[2m",
		Red: "\x1b[31m", Green: "\x1b[32m", Yellow: "\x1b[33m",
		Blue: "\x1b[34m", Magenta: "\x1b[35m", Cyan: "\x1b[36m",
		Gray: "\x1b[90m", BrightCyan: "\x1b[96m",
	}
}

// ColorEnabled reports whether ANSI color should be emitted.
//
// Honors NO_COLOR (https://no-color.org), FORCE_COLOR, and TTY-ness. Piping the
// activity view into a file must not fill it with escape codes.
func ColorEnabled(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// visibleLen counts printable width, ignoring ANSI escape sequences.
func visibleLen(s string) int {
	n, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		default:
			n++
		}
	}
	return n
}

// DisplayWidth estimates the terminal cells a string occupies.
//
// Only two things are actually double-width in practice: characters in the
// emoji planes, and East Asian wide ranges. Symbols in the miscellaneous block
// (★ ● ⌁ …) render as one cell UNLESS followed by U+FE0F, the emoji variation
// selector, which forces emoji presentation — so the scan needs a lookahead
// rather than a range test alone.
func DisplayWidth(s string) int {
	runes := []rune(s)
	w := 0
	for i, r := range runes {
		switch {
		case r == 0xFE0F || r == 0xFE0E || r == 0x200D ||
			(r >= 0x1F3FB && r <= 0x1F3FF):
			// variation selectors, ZWJ and skin-tone modifiers add no width
		case r == 0x1b:
			// escape sequences are visibleLen's job, not this one
		case i+1 < len(runes) && runes[i+1] == 0xFE0F:
			w += 2 // forced emoji presentation
		case r >= 0x1F300:
			w += 2 // emoji planes
		case isEastAsianWide(r):
			w += 2
		default:
			w++
		}
	}
	return w
}

// isEastAsianWide reports the ranges Unicode TR11 classifies as Wide.
func isEastAsianWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6:
		return true
	}
	return false
}

// PadTo right-pads s to the given display width.
func PadTo(s string, width int) string {
	if n := DisplayWidth(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

// Truncate shortens s to n display cells, adding an ellipsis when it cuts.
func Truncate(s string, n int) string { return truncate(s, n) }

// wrap breaks text into lines of at most width runes, preserving paragraphs.
func wrap(text string, width int) []string {
	if width < 20 {
		width = 20
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			out = append(out, "")
			continue
		}
		var line strings.Builder
		for _, word := range strings.Fields(para) {
			switch {
			case line.Len() == 0:
				line.WriteString(word)
			case line.Len()+1+len(word) <= width:
				line.WriteString(" ")
				line.WriteString(word)
			default:
				out = append(out, line.String())
				line.Reset()
				line.WriteString(word)
			}
		}
		if line.Len() > 0 {
			out = append(out, line.String())
		}
	}
	return out
}

// truncate shortens s to n runes, adding an ellipsis when it cuts.
func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// humanCount formats an integer with thousands separators.
func humanCount(n int) string {
	s := []byte{}
	str := itoa(n)
	for i, c := range []byte(str) {
		if i > 0 && (len(str)-i)%3 == 0 {
			s = append(s, ',')
		}
		s = append(s, c)
	}
	return string(s)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
