package google

import "strings"

// assistants maps GA4 session sources to AI assistants. Order matters: the first match
// wins, so more specific hosts come first.
var assistants = []struct{ host, name string }{
	{"chatgpt.com", "ChatGPT"},
	{"chat.openai.com", "ChatGPT"},
	{"openai.com", "ChatGPT"},
	{"perplexity.ai", "Perplexity"},
	{"gemini.google.com", "Gemini"},
	{"bard.google.com", "Gemini"},
	{"copilot.microsoft.com", "Copilot"},
	{"claude.ai", "Claude"},
	{"deepseek.com", "DeepSeek"},
	{"you.com", "You.com"},
	{"meta.ai", "Meta AI"},
}

// Assistant returns the AI assistant a GA4 session source belongs to, or "" when the
// source is not an AI assistant.
func Assistant(source string) string {
	s := strings.ToLower(strings.TrimSpace(source))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	for _, a := range assistants {
		if s == a.host || strings.HasSuffix(s, "."+a.host) {
			return a.name
		}
	}
	return ""
}
