package lark

import "github.com/anomalyco/ssh-bot/internal/render"

// InitialCardJSON returns the JSON bytes of the "just received your message"
// placeholder card sent before the agent loop produces its first event.
// Its purpose is to give the user immediate feedback (SC-001: < 3s).
//
// The card is intentionally minimal; the renderer PATCHes it in place.
func InitialCardJSON() []byte {
	return []byte(`{
  "config": {"update_multi": true, "wide_screen_mode": true},
  "header": {"template": "blue", "title": {"tag": "plain_text", "content": "AI 助手"}},
  "elements": [
    {"tag": "div", "text": {"tag": "lark_md", "content": "💭 思考中…"}}
  ]
}`)
}

// PlainTextCardJSON builds a minimal card carrying a single markdown string.
// Used for command replies (/clear, /help, etc.).
func PlainTextCardJSON(text string) []byte {
	text = render.NormalizeLarkMarkdown(text)
	body := map[string]any{
		"config": map[string]any{"update_multi": true, "wide_screen_mode": true},
		"header": map[string]any{
			"template": "blue",
			"title":    map[string]any{"tag": "plain_text", "content": "AI 助手"},
		},
		"elements": []any{
			map[string]any{
				"tag":  "div",
				"text": map[string]any{"tag": "lark_md", "content": text},
			},
		},
	}
	raw, _ := jsonMarshal(body)
	return raw
}

// jsonMarshal is extracted for testability; just a wrapper around json.Marshal.
func jsonMarshal(v any) ([]byte, error) {
	return jsonMarshalIndirect(v)
}
