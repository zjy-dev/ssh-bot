package render

import (
	"html"
	"regexp"
	"strings"
)

var (
	headingLineRE  = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.+?)\s*#*\s*$`)
	htmlBreakTagRE = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlAnyTagRE   = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	blankLineRunRE = regexp.MustCompile(`\n{3,}`)
	mdTableSplitRE = regexp.MustCompile(`^\s*\|?(?:\s*:?-+:?\s*\|)+\s*:?-+:?\s*\|?\s*$`)
)

var emojiCompatReplacer = strings.NewReplacer(
	"✔️", "✅",
	"✔", "✅",
	"✖️", "❌",
	"✖", "❌",
	"❗", "⚠️",
	"⚡", "💡",
	"✨", "💡",
	"👉", "•",
	"📍", "📌",
	"🟢", "✅",
	"🔴", "❌",
	"🟡", "⚠️",
	"⌛", "⏳",
)

// NormalizeLarkMarkdown converts general-purpose Markdown into the lark_md
// subset that Feishu cards render reliably. It prefers an AST-based conversion
// and falls back to a line-based downgrader if parsing/rendering fails.
func NormalizeLarkMarkdown(input string) string {
	input = normalizeNewlines(input)
	if strings.TrimSpace(input) == "" {
		return ""
	}
	if out, err := renderFeishuMarkdown(input); err == nil && strings.TrimSpace(out) != "" {
		return out
	}
	return normalizeLarkFallback(input)
}

// NormalizeLarkPreview is a smaller-footprint cleaner used for transient UI
// preview text such as the thinking region, where we prefer compactness over
// preserving full Markdown structure.
func NormalizeLarkPreview(input string) string {
	return normalizeLarkFallback(input)
}

func normalizeLarkFallback(input string) string {
	input = normalizeNewlines(input)
	if input == "" {
		return ""
	}
	input = sanitizeHTMLText(input)

	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")

	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	inFence := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}

		if mdTableSplitRE.MatchString(line) {
			continue
		}

		if m := headingLineRE.FindStringSubmatch(line); len(m) == 2 {
			text := strings.TrimSpace(m[1])
			if text == "" {
				out = append(out, "")
			} else {
				out = append(out, "**"+text+"**")
			}
			continue
		}

		out = append(out, strings.TrimRight(line, " \t"))
	}

	return cleanupRenderedMarkdown(strings.Join(out, "\n"))
}

func quoteMarkdownLines(s string) string {
	if s == "" {
		return ""
	}
	return "> " + strings.ReplaceAll(s, "\n", "\n> ")
}

func normalizeNewlines(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	return strings.ReplaceAll(input, "\r", "\n")
}

func sanitizeHTMLText(input string) string {
	input = htmlBreakTagRE.ReplaceAllString(input, "\n")
	input = htmlAnyTagRE.ReplaceAllString(input, "")
	return html.UnescapeString(input)
}

func cleanupRenderedMarkdown(input string) string {
	if input == "" {
		return ""
	}
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	joined := strings.Join(lines, "\n")
	joined = blankLineRunRE.ReplaceAllString(joined, "\n\n")
	return strings.TrimSpace(joined)
}

func normalizeCompatibleEmoji(input string) string {
	if input == "" {
		return ""
	}
	return emojiCompatReplacer.Replace(input)
}

func formatSectionHeading(text string) string {
	text = normalizeInlineWhitespace(text)
	if text == "" {
		return ""
	}
	return "**【" + normalizeCompatibleEmoji(text) + "】**"
}

func formatInlineCode(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "「」"
	}
	text = strings.ReplaceAll(text, "「", "[")
	text = strings.ReplaceAll(text, "」", "]")
	return "「" + text + "」"
}

func listBullet(depth int) string {
	switch depth {
	case 0:
		return "• "
	case 1:
		return "◦ "
	default:
		return "▪ "
	}
}
