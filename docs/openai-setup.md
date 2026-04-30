# OpenAI API 配置指南

## 快速开始

1. 获取 OpenAI API Key：
   - 访问 https://platform.openai.com/api-keys
   - 创建新的 API Key

2. 设置环境变量：
   ```bash
   export OPENAI_API_KEY=sk-your-api-key-here
   ```

3. 运行演示：
   ```bash
   go run examples/08_extract_demo/main.go
   ```

## 配置选项

### 环境变量

| 变量 | 必需 | 默认值 | 说明 |
|------|------|--------|------|
| `OPENAI_API_KEY` | 是 | - | OpenAI API Key |
| `OPENAI_MODEL` | 否 | `gpt-5.4-nano` | 模型名称（如 gpt-5.4-nano、gpt-4o 或其他 OpenAI 兼容模型） |
| `OPENAI_BASE_URL` | 否 | `https://api.openai.com/v1` | 自定义API端点 |
| `OPENAI_TEMPERATURE` | 否 | `0.3` | 温度参数 (0-1)，越低输出越稳定 |
| `OPENAI_TIMEOUT` | 否 | `30` | API调用超时时间（秒） |

### 示例

```bash
# 使用 GPT-4 (更强大但成本更高)
export OPENAI_API_KEY=sk-...
export OPENAI_MODEL=gpt-4

# 使用自定义端点（如 Azure OpenAI）
export OPENAI_API_KEY=your-azure-key
export OPENAI_BASE_URL=https://your-resource.openai.azure.com/openai/deployments/your-deployment

# 运行演示
go run examples/08_extract_demo/main.go
```

## 成本估算

基于 OpenAI 2024 年定价：

| 模型 | 输入 | 输出 | 估算成本/1000条对话 |
|------|------|------|---------------------|
| GPT-4o | $5/M tokens | $15/M tokens | ~$0.05-0.20 |
| GPT-4 | $30/M tokens | $60/M tokens | ~$0.30-0.80 |
| GPT-3.5 Turbo | $0.5/M tokens | $1.5/M tokens | ~$0.01-0.03 |

## 使用自定义 Prompt

你可以在代码中传入自定义提取提示词：

```go
prompt := memory.ExtractionPrompt{
    ID: "my-custom-prompt",
    SystemPrompt: `你的自定义提示词...`,
    JSONSchema: `你的JSON Schema...`,
}

req := memory.ExtractRequest{
    DialogText:        "我喜欢深色主题",
    MinConfidence:     0.7,
    ExtractionPrompt:  &prompt,
    LLMConfig: &memory.LLMConfig{
        APIKey: "sk-...",
        Model:  "gpt-4o",
    },
}
```

## 故障排除

### API Key 无效
```
OpenAI API error: Invalid API key
```
- 检查 `OPENAI_API_KEY` 是否正确设置
- 确认 API Key 有有效的余额

### 请求超时
```
API request failed: context deadline exceeded
```
- 增加 `OPENAI_TIMEOUT` 值
- 检查网络连接

### 解析错误
```
failed to parse LLM output as memories
```
- 可能是模型输出了非 JSON 格式
- 尝试降低 `OPENAI_TEMPERATURE` 到 0.1
- 或使用更强的模型如 GPT-4o

## 安全建议

1. **不要在代码中硬编码 API Key**
   - 使用环境变量
   - 使用配置文件（从外部加载）
   - 考虑使用密钥管理服务

2. **API Key 加密**
   - 生产环境中应加密存储 API Key
   - 在数据库中存储加密后的密钥
   - 应用层解密后使用

3. **访问控制**
   - 限制 API Key 的使用范围
   - 设置使用配额和告警
   - 定期轮换 API Key

## 本地模型支持（Ollama）

也可以使用本地 Ollama 模型，无需 API Key：

```bash
# 安装 Ollama
brew install ollama

# 拉取模型
ollama pull llama3.1:8b

# 配置环境
export OPENAI_BASE_URL=http://localhost:11434/v1
export OPENAI_MODEL=llama3.1:8b
export OPENAI_API_KEY=ollama  # Ollama 不验证 key，但需要一个值

# 运行
go run examples/08_extract_demo/main.go
```

注意：本地模型可能需要更多的提示词工程才能达到满意的提取效果。
