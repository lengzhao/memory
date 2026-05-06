package service

import (
	"context"
	"strings"

	"github.com/lengzhao/memory/model"
)

// ExtractPolicy configures reusable post-LLM filtering applied after the MinConfidence gate
// and before optional PostExtractHook / persistence.
type ExtractPolicy struct {
	// DropTransientEphemeral removes NamespaceTransient memories whose normalized text contains
	// ephemeral cues (wall-clock, calendar extrapolation). Useful for hosts that still want
	// transient rows but not “what time is it” noise.
	DropTransientEphemeral bool
	// TransientEphemeralSubstrings overrides substring cues (case-insensitive).
	// Empty/nil with DropTransientEphemeral uses DefaultTransientEphemeralSubstrings().
	TransientEphemeralSubstrings []string
}

// DefaultTransientEphemeralSubstrings returns built-in substring cues for noisy transient rows.
func DefaultTransientEphemeralSubstrings() []string {
	return []string{
		"当前时间", "现在时间", "utc", "今天", "现在是", "timestamp", "几点",
		"日期解析", "明天", "对应 ",
	}
}

// ApplyExtractPolicy applies policy filters to an in-memory slice (non-destructive).
func ApplyExtractPolicy(mem []ExtractedMemory, policy *ExtractPolicy) []ExtractedMemory {
	if policy == nil || !policy.DropTransientEphemeral {
		return mem
	}
	subs := policy.TransientEphemeralSubstrings
	if len(subs) == 0 {
		subs = DefaultTransientEphemeralSubstrings()
	}
	out := make([]ExtractedMemory, 0, len(mem))
	for _, m := range mem {
		if dropTransientEphemeral(m, subs) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func dropTransientEphemeral(m ExtractedMemory, subs []string) bool {
	if strings.TrimSpace(string(m.Namespace)) != string(model.NamespaceTypeTransient) {
		return false
	}
	text := normalizeExtractedMemoryText(m.Title, m.Summary, m.Content)
	if text == "" {
		return true
	}
	lt := strings.ToLower(text)
	for _, k := range subs {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if strings.Contains(lt, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

func normalizeExtractedMemoryText(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	s := b.String()
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func filterByMinConfidence(memories []ExtractedMemory, minConf float64) []ExtractedMemory {
	out := make([]ExtractedMemory, 0, len(memories))
	for _, m := range memories {
		if m.Confidence >= minConf {
			out = append(out, m)
		}
	}
	return out
}

func applyPostExtractPipeline(ctx context.Context, memories []ExtractedMemory, req ExtractRequest) ([]ExtractedMemory, error) {
	minConf := req.MinConfidence
	if minConf == 0 {
		minConf = 0.7
	}
	filtered := filterByMinConfidence(memories, minConf)
	filtered = ApplyExtractPolicy(filtered, req.ExtractPolicy)
	if req.PostExtractHook != nil {
		var err error
		filtered, err = req.PostExtractHook(ctx, filtered)
		if err != nil {
			return nil, err
		}
	}
	return filtered, nil
}
