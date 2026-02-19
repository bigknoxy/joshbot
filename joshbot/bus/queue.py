"""Async message bus with pub/sub dispatch."""

from __future__ import annotations

import asyncio
from typing import Callable, Awaitable

from loguru import logger

from .events import InboundMessage, OutboundMessage


class MessageBus:
    """Two-queue message bus decoupling channels from the agent."""

    MAX_QUEUE_SIZE = 1000  # Prevent unbounded queue growth

    def __init__(self) -> None:
        self._inbound: asyncio.Queue[InboundMessage] = asyncio.Queue(
            maxsize=self.MAX_QUEUE_SIZE
        )
        self._outbound: asyncio.Queue[OutboundMessage] = asyncio.Queue(
            maxsize=self.MAX_QUEUE_SIZE
        )
        self._inbound_handlers: list[Callable[[InboundMessage], Awaitable[None]]] = []
        self._outbound_handlers: list[Callable[[OutboundMessage], Awaitable[None]]] = []
        self._running = False

    def on_inbound(self, handler: Callable[[InboundMessage], Awaitable[None]]) -> None:
        """Register a handler for inbound messages."""
        self._inbound_handlers.append(handler)

    def on_outbound(
        self, handler: Callable[[OutboundMessage], Awaitable[None]]
    ) -> None:
        """Register a handler for outbound messages."""
        self._outbound_handlers.append(handler)

    async def publish_inbound(self, message: InboundMessage) -> bool:
        """Publish an inbound message from a channel. Returns False if queue is full."""
        if self._inbound.full():
            logger.error(f"Inbound queue full! Dropping message from {message.channel}")
            return False
        logger.debug(
            f"Inbound from {message.channel}:{message.sender_name}: {message.content[:80]}"
        )
        await self._inbound.put(message)
        return True

    async def publish_outbound(self, message: OutboundMessage) -> bool:
        """Publish an outbound message to a channel. Returns False if queue is full."""
        if self._outbound.full():
            logger.error(f"Outbound queue full! Dropping message to {message.channel}")
            return False
        logger.debug(f"Outbound to {message.channel}: {message.content[:80]}")
        await self._outbound.put(message)
        return True

    async def start(self) -> None:
        """Start processing messages."""
        self._running = True
        await asyncio.gather(
            self._process_inbound(),
            self._process_outbound(),
        )

    async def stop(self) -> None:
        """Stop processing messages."""
        self._running = False

    async def _process_inbound(self) -> None:
        """Process inbound message queue."""
        while self._running:
            try:
                message = await asyncio.wait_for(self._inbound.get(), timeout=1.0)
                for handler in self._inbound_handlers:
                    try:
                        await handler(message)
                    except Exception as e:
                        logger.error(f"Inbound handler error: {e}")
            except asyncio.TimeoutError:
                continue
            except Exception as e:
                logger.error(f"Inbound processing error: {e}")

    async def _process_outbound(self) -> None:
        """Process outbound message queue."""
        while self._running:
            try:
                message = await asyncio.wait_for(self._outbound.get(), timeout=1.0)
                for handler in self._outbound_handlers:
                    try:
                        await handler(message)
                    except Exception as e:
                        logger.error(f"Outbound handler error: {e}")
            except asyncio.TimeoutError:
                continue
            except Exception as e:
                logger.error(f"Outbound processing error: {e}")
