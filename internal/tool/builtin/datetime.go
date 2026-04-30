// Package builtin holds built-in Tool implementations.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anomalyco/ssh-bot/internal/tool"
)

// NewDatetime returns the "datetime" tool. See contracts/tools.md#datetime.
func NewDatetime() tool.Tool { return datetimeTool{} }

type datetimeTool struct{}

func (datetimeTool) Name() string { return "datetime" }

func (datetimeTool) Description() string {
	return "Get the current date/time or convert timezones. Use this instead of guessing — your training data is stale."
}

const datetimeSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "action":    {"type": "string", "enum": ["now", "convert", "add"]},
    "timezone":  {"type": "string", "default": "Asia/Shanghai"},
    "iso_input": {"type": "string"},
    "delta":     {"type": "string", "description": "ISO-8601 duration, e.g. P1D or PT2H"}
  },
  "required": ["action"],
  "additionalProperties": false
}`

func (datetimeTool) InputSchema() json.RawMessage { return json.RawMessage(datetimeSchema) }
func (datetimeTool) Source() tool.Source          { return tool.SourceBuiltin }
func (datetimeTool) Available() bool              { return true }

func (datetimeTool) Call(_ context.Context, args json.RawMessage) (tool.Result, error) {
	var req struct {
		Action   string `json:"action"`
		Timezone string `json:"timezone"`
		ISOInput string `json:"iso_input"`
		Delta    string `json:"delta"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return tool.Result{}, fmt.Errorf("invalid args: %w", err)
		}
	}
	if req.Timezone == "" {
		req.Timezone = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(req.Timezone)
	if err != nil {
		return tool.Result{}, fmt.Errorf("invalid timezone: %w", err)
	}

	switch strings.ToLower(req.Action) {
	case "now", "":
		now := time.Now().In(loc)
		return tool.Result{Content: now.Format(time.RFC3339)}, nil
	case "convert":
		if req.ISOInput == "" {
			return tool.Result{}, fmt.Errorf(`"iso_input" is required for action="convert"`)
		}
		t, err := time.Parse(time.RFC3339, req.ISOInput)
		if err != nil {
			return tool.Result{}, fmt.Errorf("parse iso_input: %w", err)
		}
		return tool.Result{Content: t.In(loc).Format(time.RFC3339)}, nil
	case "add":
		if req.ISOInput == "" || req.Delta == "" {
			return tool.Result{}, fmt.Errorf(`"iso_input" and "delta" are required for action="add"`)
		}
		t, err := time.Parse(time.RFC3339, req.ISOInput)
		if err != nil {
			return tool.Result{}, fmt.Errorf("parse iso_input: %w", err)
		}
		d, err := parseISODuration(req.Delta)
		if err != nil {
			return tool.Result{}, fmt.Errorf("parse delta: %w", err)
		}
		return tool.Result{Content: t.In(loc).Add(d).Format(time.RFC3339)}, nil
	default:
		return tool.Result{}, fmt.Errorf("unknown action %q", req.Action)
	}
}

// parseISODuration parses a subset of ISO-8601 durations sufficient for the
// datetime tool (PnD / PTnH / PTnM / PTnS and combinations).
func parseISODuration(s string) (time.Duration, error) {
	if len(s) < 2 || s[0] != 'P' {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	var total time.Duration
	i := 1
	inTime := false
	for i < len(s) {
		if s[i] == 'T' {
			inTime = true
			i++
			continue
		}
		// Read number
		start := i
		for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
			i++
		}
		if start == i || i == len(s) {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		n := 0
		for _, b := range s[start:i] {
			n = n*10 + int(b-'0')
		}
		unit := s[i]
		i++
		var d time.Duration
		switch {
		case !inTime && unit == 'D':
			d = time.Duration(n) * 24 * time.Hour
		case inTime && unit == 'H':
			d = time.Duration(n) * time.Hour
		case inTime && unit == 'M':
			d = time.Duration(n) * time.Minute
		case inTime && unit == 'S':
			d = time.Duration(n) * time.Second
		default:
			return 0, fmt.Errorf("unsupported unit %q in %q", unit, s)
		}
		total += d
	}
	return total, nil
}
