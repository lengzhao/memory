// Package service provides Chinese text tokenization using jiebago.
package service

import (
	"strings"
	"unicode"

	"github.com/lengzhao/jiebago"
)

const (
	// maxTokenizeLen limits text length for tokenization.
	// Long texts are truncated to head + tail to focus on important content.
	maxTokenizeHead = 100 // First 100 chars
	maxTokenizeTail = 100 // Last 100 chars
)

// TokenizeForSearch tokenizes text for search indexing.
// Uses jiebago's search engine mode for high recall.
// Long texts are truncated to head+tail to avoid indexing noise from quoted content.
func TokenizeForSearch(text string) string {
	if text == "" {
		return ""
	}

	// Truncate long text to head + tail
	text = truncateForTokenize(text)

	var words []string
	seen := make(map[string]bool)

	// Use jiebago.Default with search mode (high recall for search)
	for word := range jiebago.Default.CutForSearch(text, true) {
		word = strings.TrimSpace(word)
		if word == "" || seen[word] {
			continue
		}

		// Skip pure punctuation and whitespace
		if isAllPunctOrSpace(word) {
			continue
		}

		words = append(words, word)
		seen[word] = true
	}

	return strings.Join(words, " ")
}

// TokenizeQuery tokenizes user search query.
// Query is usually short, so no truncation needed.
func TokenizeQuery(query string) string {
	if query == "" {
		return ""
	}

	var words []string
	seen := make(map[string]bool)

	for word := range jiebago.Default.CutForSearch(query, true) {
		word = strings.TrimSpace(word)
		if word == "" || seen[word] {
			continue
		}
		if isAllPunctOrSpace(word) {
			continue
		}
		words = append(words, word)
		seen[word] = true
	}

	return strings.Join(words, " ")
}

// truncateForTokenize limits text length for indexing.
// For long text, keeps first 100 and last 100 chars (often contain key info).
// Example: 1000-char text with long quote in middle → head + " ... " + tail
func truncateForTokenize(text string) string {
	runes := []rune(text)
	if len(runes) <= maxTokenizeHead+maxTokenizeTail {
		return text
	}

	// Keep head and tail, discard middle (often quoted/less important content)
	head := string(runes[:maxTokenizeHead])
	tail := string(runes[len(runes)-maxTokenizeTail:])

	return head + " " + tail
}

// isAllPunctOrSpace checks if string is all punctuation or whitespace.
func isAllPunctOrSpace(s string) bool {
	for _, r := range s {
		if !unicode.IsPunct(r) && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
