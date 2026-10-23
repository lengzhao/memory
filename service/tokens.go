package service

import (
	"strings"
	"unicode"
)

// TokenCounter estimates how many model tokens a piece of text costs. It is injectable so a host
// can plug in the exact tokenizer of its model (tiktoken, a provider SDK, ...); the default is a
// dependency-free estimate that is deliberately conservative for mixed CJK/Latin text.
type TokenCounter func(text string) int

// DefaultTokenCounter approximates token counts:
//
//	CJK/other wide runes .... 1 token each
//	Latin/digit runs ........ ~4 characters per token (rounded up)
//	punctuation ............. 1 token per run
//
// It over-estimates slightly on purpose: exceeding a model context window is a hard failure,
// while leaving a few tokens unused is not.
func DefaultTokenCounter(text string) int {
	if text == "" {
		return 0
	}

	tokens := 0
	latinRun := 0
	flushLatin := func() {
		if latinRun > 0 {
			tokens += (latinRun + 3) / 4
			latinRun = 0
		}
	}

	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hangul, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			flushLatin()
			tokens++
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '\'' || r == '-' || r == '_':
			latinRun++
		case unicode.IsSpace(r):
			flushLatin()
		default:
			flushLatin()
			tokens++
		}
	}
	flushLatin()
	return tokens
}

// tokenCounter is the counter used by budgeting. Hosts that need exact accounting call
// SetTokenCounter before touching the service.
var tokenCounter TokenCounter = DefaultTokenCounter

// SetTokenCounter installs the token counter used for budget assembly and summary thresholds.
func SetTokenCounter(counter TokenCounter) {
	if counter == nil {
		tokenCounter = DefaultTokenCounter
		return
	}
	tokenCounter = counter
}

// CountTokens estimates the token cost of text with the configured counter.
func CountTokens(text string) int {
	return tokenCounter(text)
}

// trimToTokens returns the longest prefix of text that fits into maxTokens, walking rune by rune
// so multi-byte text is never cut mid-character.
func trimToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	if tokenCounter(text) <= maxTokens {
		return text
	}

	var builder strings.Builder
	used := 0
	for _, r := range text {
		cost := tokenCounter(string(r))
		if used+cost > maxTokens {
			break
		}
		builder.WriteRune(r)
		used += cost
	}
	return strings.TrimSpace(builder.String())
}
