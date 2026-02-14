"""Configuration schema for joshbot."""

from __future__ import annotations

from pathlib import Path
from typing import Any

from pydantic import BaseModel, Field
from pydantic_settings import BaseSettings


DEFAULT_HOME = Path.home() / ".joshbot"
DEFAULT_WORKSPACE = DEFAULT_HOME / "workspace"


class ProviderConfig(BaseModel):
    """Configuration for a single LLM provider."""

    api_key: str = ""
    api_base: str = ""
    extra_headers: dict[str, str] = Field(default_factory=dict)


class AgentDefaults(BaseModel):
    """Default agent configuration."""

    workspace: str = str(DEFAULT_WORKSPACE)
    model: str = "arcee-ai/trinity-large-preview:free"
    max_tokens: int = 8192
    temperature: float = 0.7
    max_tool_iterations: int = 20
    memory_window: int = 50


class AgentsConfig(BaseModel):
    """Agent configuration."""

    defaults: AgentDefaults = Field(default_factory=AgentDefaults)


class TelegramConfig(BaseModel):
    """Telegram channel configuration."""

    enabled: bool = False
    token: str = ""
    allow_from: list[str] = Field(default_factory=list)
    proxy: str = ""


class ChannelsConfig(BaseModel):
    """Channels configuration."""

    telegram: TelegramConfig = Field(default_factory=TelegramConfig)


class WebSearchConfig(BaseModel):
    """Web search tool configuration."""

    api_key: str = ""


class WebToolsConfig(BaseModel):
    """Web tools configuration."""

    search: WebSearchConfig = Field(default_factory=WebSearchConfig)


class ExecConfig(BaseModel):
    """Shell execution configuration."""

    timeout: int = 60


class ToolsConfig(BaseModel):
    """Tools configuration."""

    web: WebToolsConfig = Field(default_factory=WebToolsConfig)
    exec: ExecConfig = Field(default_factory=ExecConfig)
    restrict_to_workspace: bool = False


class GatewayConfig(BaseModel):
    """Gateway server configuration."""

    host: str = "0.0.0.0"
    port: int = 18790


class Config(BaseSettings):
    """Root configuration for joshbot."""

    model_config = {"env_prefix": "JOSHBOT_", "env_nested_delimiter": "__"}

    providers: dict[str, ProviderConfig] = Field(
        default_factory=lambda: {"openrouter": ProviderConfig()}
    )
    agents: AgentsConfig = Field(default_factory=AgentsConfig)
    channels: ChannelsConfig = Field(default_factory=ChannelsConfig)
    tools: ToolsConfig = Field(default_factory=ToolsConfig)
    gateway: GatewayConfig = Field(default_factory=GatewayConfig)

    @property
    def home_dir(self) -> Path:
        return DEFAULT_HOME

    @property
    def workspace_dir(self) -> Path:
        return Path(self.agents.defaults.workspace)

    @property
    def sessions_dir(self) -> Path:
        return self.home_dir / "sessions"

    @property
    def media_dir(self) -> Path:
        return self.home_dir / "media"

    @property
    def cron_dir(self) -> Path:
        return self.home_dir / "cron"
