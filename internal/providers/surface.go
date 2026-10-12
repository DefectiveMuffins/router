package providers

// WireSurface names the wire API a catalog binding is served on when its
// provider mounts more than one. The zero value defers to the provider's family.
type WireSurface int

const (
	// SurfaceProviderDefault serves the binding on its provider family's wire.
	SurfaceProviderDefault WireSurface = iota
	// SurfaceChatCompletions serves the binding on OpenAI chat/completions.
	SurfaceChatCompletions
	// SurfaceResponses serves the binding on OpenAI Responses only.
	SurfaceResponses
	// SurfaceMessages serves the binding on Anthropic Messages.
	SurfaceMessages
)

// Family returns the translation family a request for this surface is emitted in.
func (s WireSurface) Family(provider string) TranslationFamily {
	switch s {
	case SurfaceChatCompletions, SurfaceResponses:
		return FamilyOpenAICompat
	case SurfaceMessages:
		return FamilyAnthropic
	default:
		return FamilyFor(provider)
	}
}

// String returns the surface's log name.
func (s WireSurface) String() string {
	switch s {
	case SurfaceChatCompletions:
		return "chat_completions"
	case SurfaceResponses:
		return "responses"
	case SurfaceMessages:
		return "messages"
	default:
		return "provider_default"
	}
}
