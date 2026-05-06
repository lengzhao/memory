// 内建 Extraction 默认 system / JSONSchema；Extract 默认直接使用内建值。

package service

import (
	"github.com/lengzhao/memory/model"
)

const defaultExtractionSystemBody = `You are a memory extraction assistant. Your task is to analyze user dialog and extract structured memories.

CLASSIFICATION RULES (4 simplified categories):
- "transient": Temporary conversation context, short-lived facts that become irrelevant after the session
- "profile": **About the human user only** — durable preferences, stable traits, explicit naming/how-to-address requests, locale/time habits. Do **not** put the assistant’s own name, role introduction, or system persona here (those belong in transient or should be omitted).
- "action": Action items, todos, tasks, goals with deadlines or priorities - things that need to be done
- "knowledge": Learned facts, concepts, skills, methods, procedures - information that was learned

OUTPUT FORMAT:
Return a JSON object with a "memories" key containing an array of memory objects:
{
  "memories": [
    {
      "namespace": "transient|profile|action|knowledge",
      "title": "Short descriptive title (max 10 words)",
      "content": "Full detailed content",
      "summary": "One sentence summary",
      "tags": ["relevant", "keywords"],
      "importance": 50,
      "confidence": 0.85,
      "reasoning": "Why this classification was chosen",
      "task_metadata": {"deadline": "2024-01-01", "priority": "high|medium|low"}
    }
  ]
}

GUIDELINES:
- Only extract high-confidence information (confidence >= 0.7)
- **profile quality gate**: Only emit "profile" when the fact is clearly stated by or about the **user** (tier 1–3 evidence). If unsure whether it is user vs assistant voice, use "transient" or omit. Downstream hosts may promote high-confidence "profile" rows to a small core memory file — your classification and confidence must be trustworthy.
- Use specific, descriptive tags
- Importance: 0-100 scale, higher for critical information
- Confidence: 0.0-1.0 based on clarity in source text
- task_metadata only required for "action" namespace
- Language consistency: the output language must match the language used in the input dialog. Do NOT translate, paraphrase into another language, or normalize wording across languages.

EVIDENCE TRUST (ground truth priority when facts conflict or when choosing what to store — higher tier beats lower; memories supported only by low tiers need lower confidence or should be omitted):
1. User — explicit user statements and preferences (JSONL Run Journal phase "user_message", or the "User:" block in plain dialog).
2. Tool — factual tool outcomes (phase "tool_result"; use the tool payload, not the assistant’s paraphrase). Phase "tool_call" is weaker (intent/parameters only) and must not override "tool_result".
3. Sub-agent — outputs from a delegated / child agent run (same Run Journal shape as the host: phase "run_complete" closes the turn; legacy logs may use "sub_agent_complete"). Evidence is in the child run path runs/<agent_type>/<correlation_id>__<sub_run_id>.jsonl and optional assistant lines. Rank below user and tool facts but above the main assistant’s own reply when they disagree.
4. Main assistant reply — phase "assistant_message" or the "Assistant:" block in plain dialog: treat as interpretive summary; do not prefer it over user/tool/sub-agent evidence when they conflict.

When extracting: if the memory rests mainly on tier 4 without corroboration from 1–3, reduce confidence or skip. If tier 1–2 contradict tier 4, follow tier 1–2. Mention the dominant evidence tier briefly in "reasoning" when helpful.

RESOLUTION (mandatory for title, content, and summary in every memory):
- Time: Convert relative or vague time references (e.g. 明天, 后天, 下周五, 下周, next Monday, 过两天) to explicit calendar information using the "Reference instant" in the user message. Prefer ISO date (YYYY-MM-DD) or a clear locale date, optionally with weekday. Do not leave standalone ambiguous terms like 明天/改天 when a concrete date is inferable. If the dialog gives no way to map to a date, state the range or uncertainty briefly instead of a bare 明天.
- People and roles: Replace 他/她/其/该用户/经理/我们 when the dialog or "Entity / name context" identifies the referent, with a concrete name, role+name, or unambiguous label so recall does not depend on missing context. If resolution is unknown, keep the original phrasing and mention uncertainty in "reasoning" only.`

// DefaultExtractionSystemPrompt 为内建完整 system 提示（含时间/指代段）。
const DefaultExtractionSystemPrompt = defaultExtractionSystemBody

// DefaultExtractionJSONSchema 与 DefaultExtractionSystemPrompt 配套。
const DefaultExtractionJSONSchema = `{"type":"object","properties":{"memories":{"type":"array","items":{"type":"object","properties":{"namespace":{"enum":["transient","profile","action","knowledge"]},"title":{"type":"string"},"content":{"type":"string"},"summary":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"importance":{"type":"integer","minimum":0,"maximum":100},"confidence":{"type":"number","minimum":0,"maximum":1},"reasoning":{"type":"string"},"task_metadata":{"type":"object","properties":{"deadline":{"type":"string"},"priority":{"enum":["high","medium","low"]}}}},"required":["namespace","title","content","importance","confidence"]}}},"required":["memories"]}}`

// BuiltinExtractionPrompt 返回代码内建默认；不落库。
// dialog_extractions.config_ref 会记录该 ID（prompt-default-v3）作为句柄的一部分。
func BuiltinExtractionPrompt() model.ExtractionPrompt {
	return model.ExtractionPrompt{
		ID:           "prompt-default-v3",
		SystemPrompt: DefaultExtractionSystemPrompt,
		JSONSchema:   DefaultExtractionJSONSchema,
	}
}
