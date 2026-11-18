package llm

import "strings"

// Endpoint is the address of a provider path (always "/v1/...") under a
// connection's base address. OpenAI-compatible services, Ollama among them,
// document their base address with "/v1" at the end, Anthropic and OpenAI
// without it; people copy whichever they read. Both forms reach the same path:
// a base that already ends in "/v1" does not get a second one.
func Endpoint(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(strings.ToLower(base), "/v1") && strings.HasPrefix(path, "/v1/") {
		base = base[:len(base)-len("/v1")]
	}
	return base + path
}
