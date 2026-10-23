// Package service provides Chinese text tokenization using jiebago.
package service

import (
	"strings"
	"unicode"

	"github.com/lengzhao/jiebago"
)

// maxIndexRunes caps how much of a memory is tokenized into the search index.
//
// The cap exists to bound index size, not to filter: memories are normally far below it, so in
// practice the whole text is indexed and any keyword in it is findable. Only rows larger than the
// cap lose their tail from the index (their stored content is untouched, and the row is still
// reachable by ID).
const maxIndexRunes = 4000

// TokenizeForSearch tokenizes text for search indexing.
//
// It uses jiebago's search-engine mode, which emits both the longest and the shorter embedded words
// for each CJK span. Indexing those overlapping tokens is what lets a natural-language query match
// without any extra similarity logic: a query segmented into "并发 写 超时" matches a memory indexed
// as "并发 写 串行 busy_timeout". The result is a space-separated token stream with duplicates removed.
func TokenizeForSearch(text string) string {
	if text == "" {
		return ""
	}
	return tokenize(truncateForIndex(text))
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

// truncateForIndex bounds the text handed to the tokenizer.
func truncateForIndex(text string) string {
	runes := []rune(text)
	if len(runes) <= maxIndexRunes {
		return text
	}
	return string(runes[:maxIndexRunes])
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
