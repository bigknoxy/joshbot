"""LLM provider registry with model name prefixing."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass
class ProviderSpec:
    """Specification for an LLM provider."""

    name: str
    prefix: str  # Model name prefix for litellm routing
    env_key: str  # Environment variable name for API key
    api_base_env: str = ""  # Environment variable for custom base URL
    description: str = ""


# Provider registry - single source of truth
PROVIDERS: dict[str, ProviderSpec] = {
    "openrouter": ProviderSpec(
        name="openrouter",
        prefix="openrouter/",
        env_key="OPENROUTER_API_KEY",
        description="OpenRouter gateway - access many models with one key",
    ),
    "anthropic": ProviderSpec(
        name="anthropic",
        prefix="",
        env_key="ANTHROPIC_API_KEY",
        description="Anthropic (Claude models)",
    ),
    "openai": ProviderSpec(
        name="openai",
        prefix="",
        env_key="OPENAI_API_KEY",
        description="OpenAI (GPT models)",
    ),
    "deepseek": ProviderSpec(
        name="deepseek",
        prefix="deepseek/",
        env_key="DEEPSEEK_API_KEY",
        description="DeepSeek",
    ),
    "gemini": ProviderSpec(
        name="gemini",
        prefix="gemini/",
        env_key="GEMINI_API_KEY",
        description="Google Gemini",
    ),
    "groq": ProviderSpec(
        name="groq",
        prefix="groq/",
        env_key="GROQ_API_KEY",
        description="Groq (fast inference, also used for Whisper transcription)",
    ),
    "custom": ProviderSpec(
        name="custom",
        prefix="openai/",
        env_key="CUSTOM_API_KEY",
        api_base_env="CUSTOM_API_BASE",
        description="Any OpenAI-compatible endpoint",
    ),
    "vllm": ProviderSpec(
        name="vllm",
        prefix="hosted_vllm/",
        env_key="VLLM_API_KEY",
        api_base_env="VLLM_API_BASE",
        description="Local vLLM or any OpenAI-compatible server",
    ),
}


def get_provider_spec(name: str) -> ProviderSpec | None:
    """Get provider spec by name."""
    return PROVIDERS.get(name)


def resolve_model_name(provider_name: str, model: str) -> str:
    """Resolve a model name with the correct provider prefix.

    If the model already has a known prefix, return as-is.
    Otherwise, prepend the provider's prefix.
    """
    spec = PROVIDERS.get(provider_name)
    if not spec:
        return model

    # Check if model already has a prefix from any known provider
    for p in PROVIDERS.values():
        if p.prefix and model.startswith(p.prefix):
            return model

    # Anthropic and OpenAI models are recognized natively by litellm
    if provider_name in ("anthropic", "openai"):
        return model

    return f"{spec.prefix}{model}"
