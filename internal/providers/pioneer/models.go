package pioneer

// UpstreamModel is a model available on Pioneer's inference API.
type UpstreamModel struct {
	ID   string
	Name string
}

// StaticModels returns the curated list of chat-capable models on
// Pioneer that are interesting for proxy use. We deliberately
// exclude encoder-only models (GLiNER) and very small LLMs that
// are not practical for agentic coding.
func StaticModels() []UpstreamModel {
	return []UpstreamModel{
		// Claude family
		{ID: "claude-opus-4-7", Name: "PIO Claude Opus 4.7"},
		{ID: "claude-sonnet-4-6", Name: "PIO Claude Sonnet 4.6"},
		{ID: "claude-haiku-4-5", Name: "PIO Claude Haiku 4.5"},

		// GPT family
		{ID: "gpt-5.5", Name: "PIO GPT-5.5"},
		{ID: "gpt-5.4", Name: "PIO GPT-5.4"},
		{ID: "gpt-5.4-mini", Name: "PIO GPT-5.4 mini"},
		{ID: "gpt-5.4-nano", Name: "PIO GPT-5.4 nano"},
		{ID: "gpt-5.1", Name: "PIO GPT-5.1"},
		{ID: "gpt-5-mini", Name: "PIO GPT-5 mini"},
		{ID: "gpt-5-nano", Name: "PIO GPT-5 nano"},
		{ID: "gpt-4.1", Name: "PIO GPT-4.1"},
		{ID: "gpt-4.1-mini", Name: "PIO GPT-4.1 mini"},
		{ID: "gpt-4.1-nano", Name: "PIO GPT-4.1 nano"},
		{ID: "gpt-4o", Name: "PIO GPT-4o"},
		{ID: "gpt-4o-mini", Name: "PIO GPT-4o mini"},

		// Open-source
		{ID: "deepseek-ai/DeepSeek-V4-Pro", Name: "PIO DeepSeek V4 Pro"},
		{ID: "moonshotai/Kimi-K2.6", Name: "PIO Kimi K2.6"},
		{ID: "zai-org/GLM-5.1", Name: "PIO GLM 5.1"},
		{ID: "MiniMaxAI/MiniMax-M2.7", Name: "PIO MiniMax M2.7"},
		{ID: "openai/gpt-oss-120b", Name: "PIO GPT-OSS 120B"},
		{ID: "openai/gpt-oss-20b", Name: "PIO GPT-OSS 20B"},
		{ID: "Qwen/Qwen3-32B", Name: "PIO Qwen3 32B"},
		{ID: "Qwen/Qwen3.6-27B", Name: "PIO Qwen3.6 27B"},
		{ID: "Qwen/Qwen3.5-9B", Name: "PIO Qwen3.5 9B"},
		{ID: "meta-llama/Llama-3.2-3B-Instruct", Name: "PIO Llama 3.2 3B"},
		{ID: "meta-llama/Llama-3.1-8B-Instruct", Name: "PIO Llama 3.1 8B"},
	}
}
