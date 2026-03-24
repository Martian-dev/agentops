package tracectx

import "context"

type providerFallbackHookKey struct{}

// ProviderFallbackHook is invoked when an LLM client switches providers.
type ProviderFallbackHook func(err error)

// WithProviderFallbackHook attaches a fallback hook to a context.
func WithProviderFallbackHook(ctx context.Context, hook ProviderFallbackHook) context.Context {
	if hook == nil {
		return ctx
	}
	return context.WithValue(ctx, providerFallbackHookKey{}, hook)
}

// EmitProviderFallback invokes a provider fallback hook, if present.
func EmitProviderFallback(ctx context.Context, err error) bool {
	if ctx == nil {
		return false
	}
	hook, ok := ctx.Value(providerFallbackHookKey{}).(ProviderFallbackHook)
	if !ok || hook == nil {
		return false
	}
	hook(err)
	return true
}
