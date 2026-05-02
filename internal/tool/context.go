package tool

import "context"

type callerKey struct{}

// WithCallerOpenID annotates ctx with the human caller's Feishu open_id so
// user-scoped tools (for example OAuth-backed doc readers) can act on behalf
// of the right person without trusting model-supplied arguments.
func WithCallerOpenID(ctx context.Context, openID string) context.Context {
	if openID == "" {
		return ctx
	}
	return context.WithValue(ctx, callerKey{}, openID)
}

// CallerOpenID returns the caller open_id previously stored on ctx, if any.
func CallerOpenID(ctx context.Context) string {
	if v, ok := ctx.Value(callerKey{}).(string); ok {
		return v
	}
	return ""
}
