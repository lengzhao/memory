// Package service provides Chinese text tokenization using jiebago.
package service

import (
	"strings"
	"unicode"

	"github.com/lengzhao/jiebago"
)

const (
	// maxTokenizeLen limits how much text is tokenized for the search index.
	// Long texts are folded into head + tail, keeping both extremes searchable while
	// bounding index size. Only the index is truncated — the stored content is untouched,
	// and Recall falls back to a LIKE scan when the index yields nothing.
	maxTokenizeHead = 120
	maxTokenizeTail = 120
)

// TokenizeForSearch tokenizes text for search indexing.
// Uses jiebago's search engine mode for high recall; the result is a space-separated
// token stream with duplicates removed.
func TokenizeForSearch(text string) string {
	if text == "" {
		return ""
	}
	return tokenize(truncateForTokenize(text))
}

// TokenizeQuery tokenizes a user search query. Queries are short, so no truncation applies.
func TokenizeQuery(query string) string {
	if query == "" {
		return ""
	}
	return tokenize(query)
}

func tokenize(text string) string {
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

// truncateForTokenize limits text length for indexing.
// For long text it keeps the first and last maxTokenize* runes, which is where titles,
// summaries and conclusions usually live.
func truncateForTokenize(text string) string {
	runes := []rune(text)
	if len(runes) <= maxTokenizeHead+maxTokenizeTail {
		return text
	}

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
