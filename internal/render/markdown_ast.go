package render

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	gmext "github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
)

type markdownRenderContext struct {
	listDepth int
}

func renderFeishuMarkdown(input string) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("render markdown panic: %v", r)
		}
	}()

	source := []byte(normalizeNewlines(input))
	md := goldmark.New(goldmark.WithExtensions(gmext.GFM, gmext.DefinitionList))
	doc := md.Parser().Parse(gmtext.NewReader(source))
	return cleanupRenderedMarkdown(renderBlockChildren(doc, source, markdownRenderContext{})), nil
}

func renderBlockChildren(parent gast.Node, source []byte, ctx markdownRenderContext) string {
	parts := make([]string, 0, parent.ChildCount())
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		part := strings.TrimSpace(renderBlock(child, source, ctx))
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n\n")
}

func renderBlock(node gast.Node, source []byte, ctx markdownRenderContext) string {
	switch n := node.(type) {
	case *gast.Document:
		return renderBlockChildren(n, source, ctx)
	case *gast.Paragraph:
		return renderInlineChildren(n, source)
	case *gast.Heading:
		text := normalizeInlineWhitespace(renderPlainInlineChildren(n, source))
		if text == "" {
			return ""
		}
		return "**" + text + "**"
	case *gast.Blockquote:
		body := renderBlockChildren(n, source, ctx)
		if body == "" {
			return ""
		}
		return prefixLines(body, "> ", ">")
	case *gast.FencedCodeBlock:
		return renderCodeBlock(strings.TrimSpace(string(n.Language(source))), string(n.Lines().Value(source)))
	case *gast.CodeBlock:
		return renderCodeBlock("", string(n.Lines().Value(source)))
	case *gast.HTMLBlock:
		return strings.TrimSpace(sanitizeHTMLText(string(n.Lines().Value(source))))
	case *gast.List:
		return renderList(n, source, ctx)
	case *gast.TextBlock:
		return strings.TrimSpace(string(n.Text(source)))
	case *gast.ThematicBreak:
		return "---"
	case *extast.Table:
		return renderTable(n, source)
	case *extast.DefinitionList:
		return renderDefinitionList(n, source)
	case *extast.FootnoteList:
		return renderBlockChildren(n, source, ctx)
	case *extast.Footnote:
		return renderBlockChildren(n, source, ctx)
	default:
		if node.HasChildren() {
			return renderBlockChildren(node, source, ctx)
		}
		return strings.TrimSpace(sanitizeHTMLText(string(node.Text(source))))
	}
}

func renderInlineChildren(parent gast.Node, source []byte) string {
	var b strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		b.WriteString(renderInline(child, source))
	}
	return b.String()
}

func renderPlainInlineChildren(parent gast.Node, source []byte) string {
	var b strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		b.WriteString(renderPlainInline(child, source))
	}
	return b.String()
}

func renderInline(node gast.Node, source []byte) string {
	switch n := node.(type) {
	case *gast.Text:
		text := string(n.Text(source))
		if n.HardLineBreak() || n.SoftLineBreak() {
			text += "\n"
		}
		return text
	case *gast.String:
		return string(n.Text(source))
	case *gast.CodeSpan:
		text := strings.TrimSpace(string(n.Text(source)))
		if text == "" {
			return "``"
		}
		return "`" + text + "`"
	case *gast.Emphasis:
		inner := renderInlineChildren(n, source)
		marker := "*"
		if n.Level == 2 {
			marker = "**"
		}
		return marker + inner + marker
	case *gast.Link:
		inner := normalizeInlineWhitespace(renderInlineChildren(n, source))
		dest := strings.TrimSpace(string(n.Destination))
		if inner == "" {
			return dest
		}
		if dest == "" {
			return inner
		}
		return "[" + escapeLinkText(inner) + "](" + dest + ")"
	case *gast.AutoLink:
		label := strings.TrimSpace(string(n.Label(source)))
		if label != "" {
			return label
		}
		return strings.TrimSpace(string(n.URL(source)))
	case *gast.Image:
		alt := normalizeInlineWhitespace(renderPlainInlineChildren(n, source))
		dest := strings.TrimSpace(string(n.Destination))
		if alt == "" {
			alt = "图片"
		}
		if dest == "" {
			return alt
		}
		return alt + ": " + dest
	case *gast.RawHTML:
		return sanitizeHTMLText(string(n.Text(source)))
	case *extast.Strikethrough:
		return "~~" + renderInlineChildren(n, source) + "~~"
	case *extast.TaskCheckBox:
		if n.IsChecked {
			return "[x] "
		}
		return "[ ] "
	default:
		if node.HasChildren() {
			return renderInlineChildren(node, source)
		}
		return sanitizeHTMLText(string(node.Text(source)))
	}
}

func renderPlainInline(node gast.Node, source []byte) string {
	switch n := node.(type) {
	case *gast.Text:
		text := string(n.Text(source))
		if n.HardLineBreak() || n.SoftLineBreak() {
			text += " "
		}
		return text
	case *gast.String:
		return string(n.Text(source))
	case *gast.CodeSpan:
		text := strings.TrimSpace(string(n.Text(source)))
		if text == "" {
			return ""
		}
		return "`" + text + "`"
	case *gast.Link:
		inner := normalizeInlineWhitespace(renderPlainInlineChildren(n, source))
		if inner != "" {
			return inner
		}
		return strings.TrimSpace(string(n.Destination))
	case *gast.AutoLink:
		label := strings.TrimSpace(string(n.Label(source)))
		if label != "" {
			return label
		}
		return strings.TrimSpace(string(n.URL(source)))
	case *gast.Image:
		alt := normalizeInlineWhitespace(renderPlainInlineChildren(n, source))
		if alt != "" {
			return alt
		}
		return strings.TrimSpace(string(n.Destination))
	case *gast.RawHTML:
		return sanitizeHTMLText(string(n.Text(source)))
	case *extast.TaskCheckBox:
		if n.IsChecked {
			return "[x]"
		}
		return "[ ]"
	default:
		if node.HasChildren() {
			return renderPlainInlineChildren(node, source)
		}
		return sanitizeHTMLText(string(node.Text(source)))
	}
}

func renderCodeBlock(language, raw string) string {
	raw = strings.TrimRight(raw, "\n")
	fence := codeFenceFor(raw)
	if language != "" {
		return fence + language + "\n" + raw + "\n" + fence
	}
	return fence + "\n" + raw + "\n" + fence
}

func codeFenceFor(raw string) string {
	if strings.Contains(raw, "```") {
		return "````"
	}
	return "```"
}

func renderList(list *gast.List, source []byte, ctx markdownRenderContext) string {
	items := make([]string, 0, list.ChildCount())
	index := 1
	for child := list.FirstChild(); child != nil; child = child.NextSibling() {
		item, ok := child.(*gast.ListItem)
		if !ok {
			continue
		}
		rendered := renderListItem(item, list.IsOrdered(), index, source, ctx)
		if strings.TrimSpace(rendered) != "" {
			items = append(items, rendered)
		}
		index++
	}
	return strings.Join(items, "\n")
}

func renderListItem(item *gast.ListItem, ordered bool, index int, source []byte, ctx markdownRenderContext) string {
	baseIndent := strings.Repeat("  ", ctx.listDepth)
	marker := "- "
	if ordered {
		marker = fmt.Sprintf("%d. ", index)
	}
	continuationIndent := baseIndent + strings.Repeat(" ", len(marker))

	blocks := make([]gast.Node, 0, item.ChildCount())
	for child := item.FirstChild(); child != nil; child = child.NextSibling() {
		blocks = append(blocks, child)
	}
	if len(blocks) == 0 {
		return baseIndent + marker
	}

	first := renderListChildBlock(blocks[0], source, markdownRenderContext{listDepth: ctx.listDepth + 1})
	if strings.TrimSpace(first) == "" {
		first = baseIndent + marker
	} else if isListLike(blocks[0]) {
		first = baseIndent + marker + "\n" + indentAllLines(first, continuationIndent)
	} else {
		first = prefixLines(first, baseIndent+marker, continuationIndent)
	}

	var b strings.Builder
	b.WriteString(first)
	for _, child := range blocks[1:] {
		part := strings.TrimSpace(renderListChildBlock(child, source, markdownRenderContext{listDepth: ctx.listDepth + 1}))
		if part == "" {
			continue
		}
		b.WriteByte('\n')
		if isListLike(child) {
			b.WriteString(indentAllLines(part, continuationIndent))
			continue
		}
		b.WriteString(indentAllLines(part, continuationIndent))
	}
	return b.String()
}

func renderListChildBlock(node gast.Node, source []byte, ctx markdownRenderContext) string {
	return renderBlock(node, source, ctx)
}

func renderTable(table *extast.Table, source []byte) string {
	rows := make([]string, 0, table.ChildCount())
	for child := table.FirstChild(); child != nil; child = child.NextSibling() {
		switch n := child.(type) {
		case *extast.TableHeader:
			rows = append(rows, renderTableCells(n, source))
		case *extast.TableRow:
			rows = append(rows, renderTableRow(n, source))
		}
	}
	return strings.Join(filterEmpty(rows), "\n")
}

func renderTableRow(row *extast.TableRow, source []byte) string {
	return renderTableCells(row, source)
}

func renderTableCells(parent gast.Node, source []byte) string {
	cells := make([]string, 0, parent.ChildCount())
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		cell, ok := child.(*extast.TableCell)
		if !ok {
			continue
		}
		text := normalizeInlineWhitespace(renderPlainInlineChildren(cell, source))
		if text == "" {
			text = "-"
		}
		cells = append(cells, text)
	}
	if len(cells) == 0 {
		return ""
	}
	return "- " + strings.Join(cells, " | ")
}

func renderDefinitionList(list *extast.DefinitionList, source []byte) string {
	items := make([]string, 0, list.ChildCount())
	terms := make([]string, 0, 1)
	for child := list.FirstChild(); child != nil; child = child.NextSibling() {
		switch n := child.(type) {
		case *extast.DefinitionTerm:
			term := normalizeInlineWhitespace(renderPlainInlineChildren(n, source))
			if term != "" {
				terms = append(terms, term)
			}
		case *extast.DefinitionDescription:
			desc := normalizeInlineWhitespace(renderBlockChildren(n, source, markdownRenderContext{}))
			label := strings.Join(terms, " / ")
			switch {
			case label == "" && desc == "":
			case label == "":
				items = append(items, "- "+desc)
			case desc == "":
				items = append(items, "- "+label)
			default:
				items = append(items, "- "+label+": "+desc)
			}
			terms = terms[:0]
		}
	}
	for _, term := range terms {
		if term != "" {
			items = append(items, "- "+term)
		}
	}
	return strings.Join(filterEmpty(items), "\n")
}

func normalizeInlineWhitespace(input string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(input)), " ")
}

func prefixLines(input, firstPrefix, nextPrefix string) string {
	if input == "" {
		return ""
	}
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		prefix := nextPrefix
		if i == 0 {
			prefix = firstPrefix
		}
		if line == "" {
			lines[i] = strings.TrimRight(prefix, " ")
		} else {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func indentAllLines(input, indent string) string {
	if input == "" {
		return ""
	}
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = strings.TrimRight(indent, " ")
		} else {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

func escapeLinkText(input string) string {
	replacer := strings.NewReplacer("[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)")
	return replacer.Replace(input)
}

func filterEmpty(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func isListLike(node gast.Node) bool {
	switch node.(type) {
	case *gast.List, *extast.DefinitionList:
		return true
	default:
		return false
	}
}
