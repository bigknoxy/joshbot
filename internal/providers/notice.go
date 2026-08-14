package providers

import "context"

// FallbackNotice reports that a request was answered by a provider other than
// the one it was addressed to. It exists so the user can be told their answer
// came from a different model than configured — a silent switch reads as the
// primary working when it is not.
type FallbackNotice struct {
	From   string // provider the request was addressed to
	To     string // provider that actually answered
	Model  string // model the answering provider was asked for
	Reason string // ClassifyError of the addressed provider's failure, or "cooldown"
}

// FallbackNoticeFunc receives a FallbackNotice when a fallback answers.
type FallbackNoticeFunc func(FallbackNotice)

// noticeKey is an unexported context key type to avoid collisions.
type noticeKey struct{}

// WithFallbackNotice attaches a per-request fallback-notice callback to the
// context. It rides the context rather than MultiProvider state for the same
// reason agent.WithSink does: concurrent turns must never cross-deliver.
func WithFallbackNotice(ctx context.Context, fn FallbackNoticeFunc) context.Context {
	return context.WithValue(ctx, noticeKey{}, fn)
}

// noticeFromContext returns the callback, or nil when none is attached.
func noticeFromContext(ctx context.Context) FallbackNoticeFunc {
	fn, _ := ctx.Value(noticeKey{}).(FallbackNoticeFunc)
	return fn
}

// notifyFallback invokes the context's notice callback when a request
// addressed to one provider was answered by another. addressedErrClass is the
// classification of the addressed provider's failure this turn; empty means
// it was never dialled (deprioritized by cooldown).
func notifyFallback(ctx context.Context, addressed, answeredBy, model, addressedErrClass string) {
	if addressed == answeredBy {
		return
	}
	fn := noticeFromContext(ctx)
	if fn == nil {
		return
	}
	reason := addressedErrClass
	if reason == "" {
		reason = "cooldown"
	}
	fn(FallbackNotice{From: addressed, To: answeredBy, Model: model, Reason: reason})
}
