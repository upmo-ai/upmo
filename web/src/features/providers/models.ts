export interface ModelInfo {
  id: string;
  name: string;
  description?: string;
  input_per_mtok?: number; // USD per 1M tokens
  output_per_mtok?: number; // USD per 1M tokens
  context_window?: string;
}

export function formatMtokPrice(v: number): string {
  const fixed = v.toFixed(2);
  return `$${fixed.endsWith(".00") ? fixed.slice(0, -3) : fixed}`;
}

export function formatPricing(inPrice?: number, outPrice?: number): string {
  if (inPrice == null && outPrice == null) return "Unpriced";
  if (inPrice === 0 && outPrice === 0) return "Free";
  const inStr = inPrice == null ? "N/A" : formatMtokPrice(inPrice);
  const outStr = outPrice == null ? "N/A" : formatMtokPrice(outPrice);
  return `in ${inStr} · out ${outStr} / MTok`;
}

export const PROVIDER_MODELS: Record<string, ModelInfo[]> = {
  nebius: [
    {
      id: "deepseek-ai/DeepSeek-R1",
      name: "DeepSeek-R1",
      description: "State-of-the-art open reasoning model",
      input_per_mtok: 0.55,
      output_per_mtok: 2.19,
      context_window: "64k",
    },
    {
      id: "meta-llama/Meta-Llama-3.1-70B-Instruct",
      name: "Llama 3.1 70B Instruct",
      description: "High performance open weights instruction model",
      input_per_mtok: 0.13,
      output_per_mtok: 0.4,
      context_window: "128k",
    },
    {
      id: "Qwen/Qwen2.5-Coder-32B-Instruct",
      name: "Qwen 2.5 Coder 32B",
      description: "Specialized code generation and refactoring",
      input_per_mtok: 0.07,
      output_per_mtok: 0.2,
      context_window: "32k",
    },
  ],
  anthropic: [
    {
      id: "claude-3-7-sonnet",
      name: "Claude 3.7 Sonnet",
      description: "Hybrid reasoning and coding model with extended thinking",
      input_per_mtok: 3.0,
      output_per_mtok: 15.0,
      context_window: "200k",
    },
    {
      id: "claude-3-5-haiku",
      name: "Claude 3.5 Haiku",
      description: "Ultra-fast response with high coding accuracy",
      input_per_mtok: 0.8,
      output_per_mtok: 4.0,
      context_window: "200k",
    },
    {
      id: "claude-3-opus",
      name: "Claude 3 Opus",
      description: "Deep contextual analysis for complex tasks",
      input_per_mtok: 15.0,
      output_per_mtok: 75.0,
      context_window: "200k",
    },
  ],
  openai: [
    {
      id: "gpt-4o",
      name: "GPT-4o",
      description: "Flagship multimodal intelligence model",
      input_per_mtok: 2.5,
      output_per_mtok: 10.0,
      context_window: "128k",
    },
    {
      id: "gpt-4o-mini",
      name: "GPT-4o mini",
      description: "Affordable fast intelligence for lightweight tasks",
      input_per_mtok: 0.15,
      output_per_mtok: 0.6,
      context_window: "128k",
    },
    {
      id: "o3-mini",
      name: "o3-mini",
      description: "High speed STEM and math reasoning",
      input_per_mtok: 1.1,
      output_per_mtok: 4.4,
      context_window: "200k",
    },
    {
      id: "o1",
      name: "o1",
      description: "Complex reasoning and multi-step planning",
      input_per_mtok: 15.0,
      output_per_mtok: 60.0,
      context_window: "200k",
    },
  ],
  gemini: [
    {
      id: "gemini-2.0-flash",
      name: "Gemini 2.0 Flash",
      description: "Next-generation fast multimodal agentic model",
      input_per_mtok: 0.1,
      output_per_mtok: 0.4,
      context_window: "1M",
    },
    {
      id: "gemini-1.5-pro",
      name: "Gemini 1.5 Pro",
      description: "Frontier reasoning with 2M token context window",
      input_per_mtok: 1.25,
      output_per_mtok: 5.0,
      context_window: "2M",
    },
    {
      id: "gemini-1.5-flash",
      name: "Gemini 1.5 Flash",
      description: "Cost-efficient multimodal processing",
      input_per_mtok: 0.075,
      output_per_mtok: 0.3,
      context_window: "1M",
    },
  ],
  deepseek: [
    {
      id: "deepseek-r1",
      name: "DeepSeek-R1",
      description: "Open reasoning model rivaling OpenAI o1",
      input_per_mtok: 0.55,
      output_per_mtok: 2.19,
      context_window: "64k",
    },
    {
      id: "deepseek-v3",
      name: "DeepSeek-V3",
      description: "High-throughput general MoE model",
      input_per_mtok: 0.14,
      output_per_mtok: 0.28,
      context_window: "64k",
    },
  ],
  groq: [
    {
      id: "llama-3.3-70b-versatile",
      name: "Llama 3.3 70B Versatile",
      description: "Fast LPUs serving Llama 3.3 at hundreds of tokens/sec",
      input_per_mtok: 0.59,
      output_per_mtok: 0.79,
      context_window: "128k",
    },
    {
      id: "llama-3.1-8b-instant",
      name: "Llama 3.1 8B Instant",
      description: "Sub-100ms time-to-first-token lightweight model",
      input_per_mtok: 0.05,
      output_per_mtok: 0.08,
      context_window: "128k",
    },
  ],
  xai: [
    {
      id: "grok-2",
      name: "Grok 2",
      description: "Frontier vision and reasoning intelligence from xAI",
      input_per_mtok: 2.0,
      output_per_mtok: 10.0,
      context_window: "128k",
    },
    {
      id: "grok-2-mini",
      name: "Grok 2 mini",
      description: "Fast efficient reasoning and coding",
      input_per_mtok: 0.2,
      output_per_mtok: 1.0,
      context_window: "128k",
    },
  ],
  openrouter: [
    {
      id: "anthropic/claude-3.7-sonnet",
      name: "Claude 3.7 Sonnet (Router)",
      description: "Routed through OpenRouter API gateway",
      input_per_mtok: 3.0,
      output_per_mtok: 15.0,
      context_window: "200k",
    },
    {
      id: "openai/gpt-4o",
      name: "GPT-4o (Router)",
      description: "Routed through OpenRouter API gateway",
      input_per_mtok: 2.5,
      output_per_mtok: 10.0,
      context_window: "128k",
    },
    {
      id: "deepseek/deepseek-r1",
      name: "DeepSeek-R1 (Router)",
      description: "OpenRouter hosted DeepSeek reasoning",
      input_per_mtok: 0.55,
      output_per_mtok: 2.19,
      context_window: "64k",
    },
  ],
  mistral: [
    {
      id: "mistral-large-latest",
      name: "Mistral Large",
      description: "Top-tier reasoning and multilingual understanding",
      input_per_mtok: 2.0,
      output_per_mtok: 6.0,
      context_window: "128k",
    },
    {
      id: "codestral-latest",
      name: "Codestral",
      description: "Fluent code generation for 80+ programming languages",
      input_per_mtok: 0.3,
      output_per_mtok: 0.9,
      context_window: "256k",
    },
  ],
  together: [
    {
      id: "meta-llama/Llama-3.3-70B-Instruct-Turbo",
      name: "Llama 3.3 70B Turbo",
      description: "Ultra-fast open weights inference engine",
      input_per_mtok: 0.88,
      output_per_mtok: 0.88,
      context_window: "128k",
    },
    {
      id: "deepseek-ai/DeepSeek-R1",
      name: "DeepSeek-R1 (Together)",
      description: "High performance serverless reasoning",
      input_per_mtok: 0.55,
      output_per_mtok: 2.19,
      context_window: "64k",
    },
  ],
  cerebras: [
    {
      id: "llama3.3-70b",
      name: "Llama 3.3 70B",
      description: "World-record token generation speed on Wafer-Scale chips",
      input_per_mtok: 0.6,
      output_per_mtok: 0.6,
      context_window: "128k",
    },
    {
      id: "llama3.1-8b",
      name: "Llama 3.1 8B",
      description: "Near-instantaneous output streaming",
      input_per_mtok: 0.1,
      output_per_mtok: 0.1,
      context_window: "128k",
    },
  ],
  fireworks: [
    {
      id: "accounts/fireworks/models/llama-v3p3-70b-instruct",
      name: "Llama 3.3 70B Instruct",
      description: "Fine-tuning and inference platform model",
      input_per_mtok: 0.9,
      output_per_mtok: 0.9,
      context_window: "128k",
    },
    {
      id: "accounts/fireworks/models/deepseek-r1",
      name: "DeepSeek-R1 (Fireworks)",
      description: "Compound AI reasoning deployment",
      input_per_mtok: 0.55,
      output_per_mtok: 2.19,
      context_window: "64k",
    },
  ],
  perplexity: [
    {
      id: "sonar-pro",
      name: "Sonar Pro",
      description: "Search-augmented reasoning with live citations",
      input_per_mtok: 3.0,
      output_per_mtok: 15.0,
      context_window: "200k",
    },
    {
      id: "sonar",
      name: "Sonar",
      description: "Fast grounded search answering",
      input_per_mtok: 1.0,
      output_per_mtok: 1.0,
      context_window: "128k",
    },
    {
      id: "sonar-reasoning",
      name: "Sonar Reasoning",
      description: "Chain of thought search and analysis",
      input_per_mtok: 1.0,
      output_per_mtok: 5.0,
      context_window: "128k",
    },
  ],
  cohere: [
    {
      id: "command-r-plus-08-2024",
      name: "Command R+",
      description: "Optimized for enterprise RAG and multi-step workflows",
      input_per_mtok: 2.5,
      output_per_mtok: 10.0,
      context_window: "128k",
    },
    {
      id: "command-r-08-2024",
      name: "Command R",
      description: "High-efficiency business reasoning and search retrieval",
      input_per_mtok: 0.15,
      output_per_mtok: 0.6,
      context_window: "128k",
    },
  ],
  ollama: [
    {
      id: "llama3.3:70b",
      name: "Llama 3.3 70B (Local)",
      description: "Running locally via Ollama runtime",
      input_per_mtok: 0,
      output_per_mtok: 0,
      context_window: "128k",
    },
    {
      id: "deepseek-r1:14b",
      name: "DeepSeek-R1 14B (Local)",
      description: "Distilled reasoning on local hardware",
      input_per_mtok: 0,
      output_per_mtok: 0,
      context_window: "64k",
    },
    {
      id: "qwen2.5-coder:32b",
      name: "Qwen 2.5 Coder 32B (Local)",
      description: "Local coding model",
      input_per_mtok: 0,
      output_per_mtok: 0,
      context_window: "32k",
    },
  ],
  lmstudio: [
    {
      id: "local-model",
      name: "LM Studio Loaded Model",
      description: "Active model loaded in local LM Studio runtime",
      input_per_mtok: 0,
      output_per_mtok: 0,
      context_window: "Variable",
    },
  ],
};

export function getProviderModels(providerId: string): ModelInfo[] {
  return PROVIDER_MODELS[providerId] ?? [];
}
