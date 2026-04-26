// 内建 Extraction 默认 system / JSONSchema；Extract 默认直接使用内建值。

package service

import (
	"github.com/lengzhao/memory/model"
)

const defaultExtractionSystemBody = `You are a memory extraction assistant. Your task is to analyze user dialog and extract structured memories.

CLASSIFICATION RULES (4 simplified categories):
- "transient": Temporary conversation context, short-lived facts that become irrelevant after the session
- "profile": User preferences, personal information, habits, likes/dislikes - long-term stable traits
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
- Use specific, descriptive tags
- Importance: 0-100 scale, higher for critical information
- Confidence: 0.0-1.0 based on clarity in source text
- task_metadata only required for "action" namespace
- Language consistency: the output language must match the language used in the input dialog. Do NOT translate, paraphrase into another language, or normalize wording across languages.

RESOLUTION (mandatory for title, content, and summary in every memory):
- Time: Convert relative or vague time references (e.g. 明天, 后天, 下周五, 下周, next Monday, 过两天) to explicit calendar information using the "Reference instant" in the user message. Prefer ISO date (YYYY-MM-DD) or a clear locale date, optionally with weekday. Do not leave standalone ambiguous terms like 明天/改天 when a concrete date is inferable. If the dialog gives no way to map to a date, state the range or uncertainty briefly instead of a bare 明天.
- People and roles: Replace 他/她/其/该用户/经理/我们 when the dialog or "Entity / name context" identifies the referent, with a concrete name, role+name, or unambiguous label so recall does not depend on missing context. If resolution is unknown, keep the original phrasing and mention uncertainty in "reasoning" only.`

// DefaultExtractionSystemPrompt 为内建完整 system 提示（含时间/指代段）。
const DefaultExtractionSystemPrompt = defaultExtractionSystemBody

// DefaultExtractionJSONSchema 与 DefaultExtractionSystemPrompt 配套。
const DefaultExtractionJSONSchema = `{"type":"object","properties":{"memories":{"type":"array","items":{"type":"object","properties":{"namespace":{"enum":["transient","profile","action","knowledge"]},"title":{"type":"string"},"content":{"type":"string"},"summary":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"importance":{"type":"integer","minimum":0,"maximum":100},"confidence":{"type":"number","minimum":0,"maximum":1},"reasoning":{"type":"string"},"task_metadata":{"type":"object","properties":{"deadline":{"type":"string"},"priority":{"enum":["high","medium","low"]}}}},"required":["namespace","title","content","importance","confidence"]}}},"required":["memories"]}}`

// BuiltinExtractionPrompt 返回代码内建默认；不落库。
// dialog_extractions.config_ref 会记录该 ID（prompt-default-v1）作为句柄的一部分。
func BuiltinExtractionPrompt() model.ExtractionPrompt {
	return model.ExtractionPrompt{
		ID:           "prompt-default-v1",
		SystemPrompt: DefaultExtractionSystemPrompt,
		JSONSchema:   DefaultExtractionJSONSchema,
	}
}
