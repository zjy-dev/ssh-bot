package render

import "testing"

func TestNormalizeLarkMarkdown_HeadingBecomesBold(t *testing.T) {
	out := NormalizeLarkMarkdown("#### 标题内容\n\n正文")
	if out != "**标题内容**\n\n正文" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestNormalizeLarkMarkdown_CodeFencePreserved(t *testing.T) {
	in := "```go\n#### not a heading\nfmt.Println(\"hi\")\n```"
	out := NormalizeLarkMarkdown(in)
	if out != in {
		t.Fatalf("code fence changed:\nwant: %q\n got: %q", in, out)
	}
}

func TestNormalizeLarkMarkdown_TableDowngradesToListLines(t *testing.T) {
	in := "| 姓名 | 角色 |\n| --- | --- |\n| Alice | Owner |\n| Bob | Dev |"
	out := NormalizeLarkMarkdown(in)
	want := "- 姓名 | 角色\n- Alice | Owner\n- Bob | Dev"
	if out != want {
		t.Fatalf("unexpected table output:\nwant: %q\n got: %q", want, out)
	}
}

func TestNormalizeLarkMarkdown_LinkAndInlineCodePreserved(t *testing.T) {
	in := "参考 [文档](https://example.com) 与 `code()`"
	out := NormalizeLarkMarkdown(in)
	if out != in {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestNormalizeLarkMarkdown_StripsHTMLButKeepsText(t *testing.T) {
	in := "<div>hello<br>world</div>"
	out := NormalizeLarkMarkdown(in)
	if out != "hello\nworld" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestNormalizeLarkPreview_IsCompact(t *testing.T) {
	in := "#### 标题\n\n<div>内容</div>"
	out := NormalizeLarkPreview(in)
	if out != "**标题**\n\n内容" {
		t.Fatalf("unexpected preview output: %q", out)
	}
}
