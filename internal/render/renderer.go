package render

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/log"
)

// ErrRateLimited is the sentinel that Sender.Patch returns when Feishu responds
// with 230020 (per-message frequency limit). The renderer back-offs on it.
var ErrRateLimited = errors.New("feishu: card update rate-limited")

// Sender is the minimal Feishu abstraction the renderer uses. Implemented by
// internal/lark.Sender. Kept here as an interface for testability.
type Sender interface {
	Patch(ctx context.Context, messageID string, cardJSON []byte) error
	ReplyInThread(ctx context.Context, rootMessageID, text string) error
}

// Renderer turns a stream of llm.StreamEvents into batched Feishu card PATCHes.
//
// Usage (one Feed per agent turn):
//
//	r := render.New(sender, logger)
//	r.Feed(ctx, messageID, events)
//	r.Stop(ctx, messageID)
type Renderer struct {
	sender Sender
	logger *slog.Logger

	// Runtime (per Feed).
	mu        sync.Mutex
	state     *State
	interval  time.Duration
	backedOff bool
}

// Interval: Feishu per-message cap is 5 QPS → minimum 200ms. We pick 250ms
// (D4). On rate-limit we back off to 500ms.
const (
	DefaultFlushInterval = 250 * time.Millisecond
	BackoffInterval      = 500 * time.Millisecond
)

// New returns a Renderer ready for use.
func New(sender Sender, logger *slog.Logger) *Renderer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Renderer{
		sender:   sender,
		logger:   logger,
		state:    NewState(),
		interval: DefaultFlushInterval,
	}
}

// State returns the renderer's mutable state (for callers that need to stamp
// TraceID / Model at start).
func (r *Renderer) State() *State { return r.state }

// Feed consumes events from an agent run and periodically PATCHes the card.
// Returns after the events channel closes. Must be called from exactly one
// goroutine.
func (r *Renderer) Feed(ctx context.Context, messageID string, events <-chan llm.StreamEvent) error {
	logger := log.FromContext(ctx, r.logger)
	var (
		dirty    bool
		lastSnap []byte
	)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	flush := func(force bool) {
		r.mu.Lock()
		if !dirty && !force {
			r.mu.Unlock()
			return
		}
		snap := r.state.Snapshot()
		r.mu.Unlock()

		body, err := BuildCardJSON(snap)
		if err != nil {
			logger.Error("render: build card json", "err", err.Error())
			return
		}
		// Skip sending if content is byte-identical to the previous flush.
		if !force && bytesEq(body, lastSnap) {
			return
		}

		perr := r.sender.Patch(ctx, messageID, body)
		if errors.Is(perr, ErrRateLimited) {
			// Back off to 500ms for the remainder of the run.
			if !r.backedOff {
				r.backedOff = true
				ticker.Reset(BackoffInterval)
				logger.Warn("render: feishu rate-limit; backing off", "new_interval", BackoffInterval.String())
			}
			return
		}
		if perr != nil {
			logger.Error("render: patch card", "err", perr.Error())
			return
		}
		lastSnap = body
		dirty = false
	}

	for {
		select {
		case <-ctx.Done():
			flush(true)
			return ctx.Err()
		case <-ticker.C:
			flush(false)
		case ev, ok := <-events:
			if !ok {
				// Stream ended.
				flush(true)
				return nil
			}
			firstText := ev.Type == llm.EventTextDelta && !r.state.ThinkingDone
			r.mu.Lock()
			changed := r.state.Apply(ev)
			r.mu.Unlock()
			if changed {
				dirty = true
			}
			// Force-flush on specific transition-critical events.
			if firstText {
				// First text — give user immediate feedback.
				flush(true)
			}
			if ev.Type == llm.EventToolCallEnd || ev.Type == llm.EventError || ev.Type == llm.EventMessageEnd {
				flush(true)
			}
		}
	}
}

// Stop marks the state done and writes a final snapshot.
func (r *Renderer) Stop(ctx context.Context, messageID string) error {
	r.mu.Lock()
	r.state.MarkDone()
	snap := r.state.Snapshot()
	r.mu.Unlock()

	body, err := BuildCardJSON(snap)
	if err != nil {
		return err
	}
	return r.sender.Patch(ctx, messageID, body)
}

// SplitLongBody determines whether the accumulated text exceeds the per-card
// soft cap and should be continued in threaded replies. Used by US1 T052.
func (r *Renderer) SplitLongBody() (inCard string, threaded []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	full := r.state.Text.String()
	const soft = 20_000
	if len(full) <= soft {
		return full, nil
	}
	inCard = full[:soft]
	rest := full[soft:]
	// Split rest into ~18KB chunks.
	const chunk = 18_000
	for len(rest) > chunk {
		// Split on the nearest newline to avoid mid-word breaks.
		cut := chunk
		if nl := indexByteFromEnd(rest[:chunk], '\n'); nl > 0 {
			cut = nl
		}
		threaded = append(threaded, rest[:cut])
		rest = rest[cut:]
	}
	if rest != "" {
		threaded = append(threaded, rest)
	}
	return inCard, threaded
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func indexByteFromEnd(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}
