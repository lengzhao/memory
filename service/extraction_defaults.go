// 内建 Extraction 默认 system / JSONSchema；未配置 extraction_prompts 时 Extract 在内存中沿用。

package service

import (
	"time"

	"github.com/lengzhao/memory/model"
)

// DefaultExtractionPromptID 是内建/占位提示在 dialog_extractions.prompt_id 中使用的 id（可不存在于 extraction_prompts 表）。
const DefaultExtractionPromptID = "prompt-default-v1"

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

RESOLUTION (mandatory for title, content, and summary in every memory):
- Time: Convert relative or vague time references (e.g. 明天, 后天, 下周五, 下周, next Monday, 过两天) to explicit calendar information using the "Reference instant" in the user message. Prefer ISO date (YYYY-MM-DD) or a clear locale date, optionally with weekday. Do not leave standalone ambiguous terms like 明天/改天 when a concrete date is inferable. If the dialog gives no way to map to a date, state the range or uncertainty briefly instead of a bare 明天.
- People and roles: Replace 他/她/其/该用户/经理/我们 when the dialog or "Entity / name context" identifies the referent, with a concrete name, role+name, or unambiguous label so recall does not depend on missing context. If resolution is unknown, keep the original phrasing and mention uncertainty in "reasoning" only.`

// DefaultExtractionSystemPrompt 为内建完整 system 提示（含时间/指代段）。
const DefaultExtractionSystemPrompt = defaultExtractionSystemBody

// DefaultExtractionJSONSchema 与 DefaultExtractionSystemPrompt 配套。
const DefaultExtractionJSONSchema = `{"type":"object","properties":{"memories":{"type":"array","items":{"type":"object","properties":{"namespace":{"enum":["transient","profile","action","knowledge"]},"title":{"type":"string"},"content":{"type":"string"},"summary":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"importance":{"type":"integer","minimum":0,"maximum":100},"confidence":{"type":"number","minimum":0,"maximum":1},"reasoning":{"type":"string"},"task_metadata":{"type":"object","properties":{"deadline":{"type":"string"},"priority":{"enum":["high","medium","low"]}}}},"required":["namespace","title","content","importance","confidence"]}}},"required":["memories"]}}`

// BuiltinExtractionPrompt 返回代码内建默认，当未指定 PromptID 且库中无 is_default 行时使用；不落库。
// dialog_extractions.prompt_id 会记录为 DefaultExtractionPromptID 作为句柄。
func BuiltinExtractionPrompt() model.ExtractionPrompt {
	now := time.Now()
	return model.ExtractionPrompt{
		ID:           DefaultExtractionPromptID,
		Name:         "v1-simplified-4cat",
		Version:      1,
		SystemPrompt: DefaultExtractionSystemPrompt,
		JSONSchema:   DefaultExtractionJSONSchema,
		IsDefault:    true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}
