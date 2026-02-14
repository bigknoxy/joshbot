"""LiteLLM-based unified LLM provider."""

from __future__ import annotations

import json
import os
from typing import Any

from loguru import logger

from ..config.schema import Config, ProviderConfig
from .base import LLMProvider, LLMResponse, ToolCallRequest
from .registry import PROVIDERS, resolve_model_name


class LiteLLMProvider(LLMProvider):
    """Unified LLM provider using litellm for multi-provider support."""

    def __init__(self, config: Config) -> None:
        self._config = config
        self._setup_env()

    def _setup_env(self) -> None:
        """Set environment variables for all configured providers."""
        for name, provider_config in self._config.providers.items():
            spec = PROVIDERS.get(name)
            if not spec:
                logger.warning(f"Unknown provider: {name}")
                continue

            if provider_config.api_key:
                os.environ[spec.env_key] = provider_config.api_key
                logger.debug(f"Set {spec.env_key} for provider {name}")

            if provider_config.api_base and spec.api_base_env:
                os.environ[spec.api_base_env] = provider_config.api_base

    def _detect_provider(self, model: str) -> str:
        """Detect which provider to use based on model name."""
        # Check if model has an explicit provider prefix
        for name, spec in PROVIDERS.items():
            if spec.prefix and model.startswith(spec.prefix):
                return name

        # Detect by model naming patterns
        if model.startswith("claude"):
            return "anthropic"
        if model.startswith(("gpt-", "o1-", "o3-")):
            return "openai"

        # Fall back to first configured provider
        for name in self._config.providers:
            if self._config.providers[name].api_key:
                return name

        return "openrouter"

    async def chat(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]] | None = None,
        model: str = "",
        max_tokens: int = 8192,
        temperature: float = 0.7,
    ) -> LLMResponse:
        """Send a chat completion request via litellm."""
        import litellm

        if not model:
            model = self._config.agents.defaults.model

        provider = self._detect_provider(model)
        resolved_model = resolve_model_name(provider, model)

        # Build kwargs
        kwargs: dict[str, Any] = {
            "model": resolved_model,
            "messages": messages,
            "max_tokens": max_tokens,
            "temperature": temperature,
        }

        if tools:
            kwargs["tools"] = tools
            kwargs["tool_choice"] = "auto"

        # Set API base if configured
        provider_config = self._config.providers.get(provider)
        if provider_config and provider_config.api_base:
            kwargs["api_base"] = provider_config.api_base
        if provider_config and provider_config.extra_headers:
            kwargs["extra_headers"] = provider_config.extra_headers

        try:
            logger.debug(f"LLM call: model={resolved_model}, messages={len(messages)}")
            response = await litellm.acompletion(**kwargs)
            return self._parse_response(response)
        except Exception as e:
            logger.error(f"LLM call failed: {e}")
            raise

    def _parse_response(self, response: Any) -> LLMResponse:
        """Parse litellm response into our LLMResponse."""
        choice = response.choices[0]
        message = choice.message

        content = message.content or ""
        tool_calls: list[ToolCallRequest] = []

        if message.tool_calls:
            for tc in message.tool_calls:
                try:
                    args = (
                        json.loads(tc.function.arguments)
                        if isinstance(tc.function.arguments, str)
                        else tc.function.arguments
                    )
                except json.JSONDecodeError:
                    args = {"raw": tc.function.arguments}

                tool_calls.append(
                    ToolCallRequest(
                        id=tc.id,
                        name=tc.function.name,
                        arguments=args or {},
                    )
                )

        usage = {}
        if response.usage:
            usage = {
                "prompt_tokens": response.usage.prompt_tokens or 0,
                "completion_tokens": response.usage.completion_tokens or 0,
                "total_tokens": response.usage.total_tokens or 0,
            }

        return LLMResponse(
            content=content,
            tool_calls=tool_calls,
            finish_reason=choice.finish_reason or "",
            usage=usage,
        )

    async def transcribe(self, audio_path: str) -> str:
        """Transcribe audio using Groq Whisper."""
        try:
            import httpx

            groq_config = self._config.providers.get("groq")
            if not groq_config or not groq_config.api_key:
                return "[Transcription unavailable - no Groq API key configured]"

            async with httpx.AsyncClient() as client:
                with open(audio_path, "rb") as f:
                    response = await client.post(
                        "https://api.groq.com/openai/v1/audio/transcriptions",
                        headers={"Authorization": f"Bearer {groq_config.api_key}"},
                        files={"file": (audio_path, f, "audio/ogg")},
                        data={"model": "whisper-large-v3"},
                        timeout=30.0,
                    )
                    response.raise_for_status()
                    return response.json().get("text", "")
        except Exception as e:
            logger.error(f"Transcription failed: {e}")
            return f"[Transcription failed: {e}]"
