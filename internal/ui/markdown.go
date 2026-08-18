package ui

import (
	"regexp"
	"strings"
)

// Answers come back as Markdown. Rendering it is strictly a terminal
// affordance: when stdout is not a terminal the text is emitted byte for byte,
// so piping, redirecting and diffing all see exactly what the API returned.

var (
	mdBold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdCode     = regexp.MustCompile("`([^`]+)`")
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdBullet   = regexp.MustCompile(`^(\s*)[-*]\s+`)
	mdNumbered = regexp.MustCompile(`^(\s*)(\d+)\.\s+`)
	// RE2 has no backreferences, so each rule character is spelled out.
	mdRule      = regexp.MustCompile(`^\s*(-{3,}|\*{3,}|_{3,})\s*$`)
	mdBlockCode = "```"
)

// RenderMarkdown converts a Markdown answer to ANSI for terminal display.
//
// It handles what the model actually emits — bold, inline code, links,
// headings, lists, fenced code — and leaves anything else alone. It never
// reflows text: terminals soft-wrap, and hard wrapping would break copy-paste.
func RenderMarkdown(text string, color bool) string {
	if !color {
		return text
	}
	p := newPalette(true)

	var out []string
	inCode := false

	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), mdBlockCode) {
			inCode = !inCode
			// The fence itself is noise once the block is styled.
			continue
		}
		if inCode {
			out = append(out, p.Dim+"  "+line+p.Reset)
			continue
		}
		if mdRule.MatchString(line) {
			out = append(out, p.Gray+strings.Repeat("─", 40)+p.Reset)
			continue
		}
		if m := mdHeading.FindStringSubmatch(line); m != nil {
			out = append(out, p.Bold+p.Yellow+inlineMarkdown(m[2], p)+p.Reset)
			continue
		}
		if m := mdBullet.FindStringSubmatch(line); m != nil {
			rest := line[len(m[0]):]
			out = append(out, m[1]+p.Cyan+"•"+p.Reset+" "+inlineMarkdown(rest, p))
			continue
		}
		if m := mdNumbered.FindStringSubmatch(line); m != nil {
			rest := line[len(m[0]):]
			out = append(out, m[1]+p.Cyan+m[2]+"."+p.Reset+" "+inlineMarkdown(rest, p))
			continue
		}
		if strings.HasPrefix(line, "> ") {
			out = append(out, p.Gray+"│ "+inlineMarkdown(line[2:], p)+p.Reset)
			continue
		}
		out = append(out, inlineMarkdown(line, p))
	}
	return strings.Join(out, "\n")
}

// inlineMarkdown styles spans within a line.
func inlineMarkdown(s string, p Palette) string {
	// Links first: their label may itself contain bold or code, and rewriting
	// the label afterwards would corrupt the escape sequence.
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		parts := mdLink.FindStringSubmatch(m)
		return hyperlink(parts[2], parts[1], p)
	})
	s = mdCode.ReplaceAllString(s, p.BrightCyan+"$1"+p.Reset)
	s = mdBold.ReplaceAllString(s, p.Bold+"$1"+p.Reset)
	return s
}

// hyperlink emits an OSC 8 terminal hyperlink, so the label is clickable and
// the URL stays out of the way.
//
// Terminals that do not implement OSC 8 ignore the sequence and print the
// label, which is why the URL is not also written inline — it would be
// duplicated everywhere that does support it.
func hyperlink(url, label string, p Palette) string {
	const (
		start = "\x1b]8;;"
		sep   = "\x1b\\"
	)
	return start + url + sep + p.Blue + label + p.Reset + start + sep
}
