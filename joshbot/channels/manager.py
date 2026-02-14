"""Channel manager for routing and dispatching messages."""

from __future__ import annotations

from typing import Any, TYPE_CHECKING

from loguru import logger

from ..bus.events import OutboundMessage
from ..bus.queue import MessageBus
from ..config.schema import Config
from .base import BaseChannel

if TYPE_CHECKING:
    from ..providers.litellm_provider import LiteLLMProvider


class ChannelManager:
    """Manages all chat channels, handles routing and dispatch."""

    def __init__(self, config: Config, bus: MessageBus) -> None:
        self._config = config
        self._bus = bus
        self._channels: dict[str, BaseChannel] = {}

    def register(self, channel: BaseChannel) -> None:
        """Register a channel."""
        self._channels[channel.name] = channel
        logger.info(f"Registered channel: {channel.name}")

    def setup_channels(self, transcriber: "LiteLLMProvider | None" = None) -> None:
        """Initialize all configured channels."""
        # Telegram
        if self._config.channels.telegram.enabled:
            from .telegram import TelegramChannel

            telegram = TelegramChannel(
                bus=self._bus,
                config=self._config.channels.telegram,
                media_dir=str(self._config.media_dir),
                transcriber=transcriber,
            )
            self.register(telegram)
            logger.info("Telegram channel configured")

        # CLI is always available but registered separately when needed
        # (since it blocks the event loop with input)

    async def start_all(self) -> None:
        """Start all registered channels."""
        for name, channel in self._channels.items():
            try:
                await channel.start()
                logger.info(f"Started channel: {name}")
            except Exception as e:
                logger.error(f"Failed to start channel {name}: {e}")

    async def stop_all(self) -> None:
        """Stop all registered channels."""
        for name, channel in self._channels.items():
            try:
                await channel.stop()
                logger.info(f"Stopped channel: {name}")
            except Exception as e:
                logger.error(f"Error stopping channel {name}: {e}")

    async def dispatch_outbound(self, message: OutboundMessage) -> None:
        """Dispatch an outbound message to the correct channel."""
        channel = self._channels.get(message.channel)
        if channel:
            try:
                await channel.send(message.channel_id, message.content)
            except Exception as e:
                logger.error(f"Failed to dispatch to {message.channel}: {e}")
        else:
            logger.warning(f"No channel registered for: {message.channel}")

    def get_channel(self, name: str) -> BaseChannel | None:
        """Get a channel by name."""
        return self._channels.get(name)

    @property
    def active_channels(self) -> list[str]:
        """List active channel names."""
        return list(self._channels.keys())
