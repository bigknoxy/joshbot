"""Telegram channel using python-telegram-bot with long polling."""

from __future__ import annotations

import asyncio
import re
from pathlib import Path
from typing import Any, TYPE_CHECKING

from loguru import logger

from ..bus.events import InboundMessage, OutboundMessage
from ..bus.queue import MessageBus
from ..config.schema import TelegramConfig
from .base import BaseChannel

if TYPE_CHECKING:
    from ..providers.litellm_provider import LiteLLMProvider


class TelegramChannel(BaseChannel):
    """Telegram bot channel with long polling."""

    def __init__(
        self,
        bus: MessageBus,
        config: TelegramConfig,
        media_dir: str | Path = "",
        transcriber: "LiteLLMProvider | None" = None,
    ) -> None:
        super().__init__(bus)
        self._config = config
        self._media_dir = (
            Path(media_dir) if media_dir else Path.home() / ".joshbot" / "media"
        )
        self._media_dir.mkdir(parents=True, exist_ok=True)
        self._transcriber = transcriber
        self._app: Any = None
        self._typing_tasks: dict[int, asyncio.Task] = {}

    @property
    def name(self) -> str:
        return "telegram"

    def is_allowed(self, sender_id: str) -> bool:
        """Check if sender is in the allowlist."""
        if not self._config.allow_from:
            return True  # No allowlist = allow all
        return str(sender_id) in self._config.allow_from

    async def start(self) -> None:
        """Start Telegram bot with long polling."""
        if not self._config.token:
            logger.warning("Telegram token not configured, skipping")
            return

        from telegram import Bot, Update
        from telegram.ext import (
            Application,
            CommandHandler,
            MessageHandler,
            filters,
        )
        from telegram.request import HTTPXRequest

        # Build application
        # Note: read_timeout must be > getUpdates timeout (30s) to avoid context deadline exceeded
        request_kwargs = {
            "connection_pool_size": 16,
            "connect_timeout": 10.0,
            "read_timeout": 60.0,  # Increased to prevent context deadline exceeded
            "write_timeout": 30.0,
        }

        builder = Application.builder().token(self._config.token)

        if self._config.proxy:
            builder = builder.proxy(self._config.proxy).get_updates_proxy(
                self._config.proxy
            )

        request = HTTPXRequest(**request_kwargs)
        builder = builder.request(request)

        self._app = builder.build()

        # Register handlers
        self._app.add_handler(CommandHandler("start", self._cmd_start))
        self._app.add_handler(CommandHandler("new", self._cmd_new))
        self._app.add_handler(CommandHandler("help", self._cmd_help))
        self._app.add_handler(CommandHandler("status", self._cmd_status))
        self._app.add_handler(
            MessageHandler(
                filters.TEXT
                | filters.PHOTO
                | filters.VOICE
                | filters.AUDIO
                | filters.Document.ALL,
                self._on_message,
            )
        )

        # Register for outbound messages
        self._bus.on_outbound(self._handle_outbound)

        # Set bot commands
        try:
            await self._app.bot.set_my_commands(
                [
                    ("start", "Start a conversation"),
                    ("new", "Start fresh conversation"),
                    ("help", "Show help"),
                    ("status", "Show status"),
                ]
            )
        except Exception as e:
            logger.warning(f"Failed to set bot commands: {e}")

        # Start polling
        await self._app.initialize()
        await self._app.start()
        await self._app.updater.start_polling(drop_pending_updates=True)
        logger.info("Telegram channel started")

    async def stop(self) -> None:
        """Stop the Telegram bot."""
        # Cancel typing tasks
        for task in self._typing_tasks.values():
            task.cancel()
        self._typing_tasks.clear()

        if self._app:
            try:
                await self._app.updater.stop()
                await self._app.stop()
                await self._app.shutdown()
            except Exception as e:
                logger.warning(f"Error stopping Telegram: {e}")
        logger.info("Telegram channel stopped")

    def _cleanup_stale_typing_tasks(self, max_age_seconds: float = 300) -> None:
        """Clean up typing tasks for inactive chats to prevent memory leaks."""
        stale_chats = []
        for chat_id, task in self._typing_tasks.items():
            if task.done():
                stale_chats.append(chat_id)
        for chat_id in stale_chats:
            del self._typing_tasks[chat_id]
            logger.debug(f"Cleaned up stale typing task for chat {chat_id}")

    async def send(self, channel_id: str, content: str, max_retries: int = 3) -> bool:
        """Send a message to a Telegram chat with retry logic."""
        if not self._app:
            return False

        # Extract chat_id from channel_id (format: "telegram:12345")
        chat_id = channel_id.split(":", 1)[-1] if ":" in channel_id else channel_id

        # Stop typing indicator
        self._stop_typing(int(chat_id))

        # Convert markdown to Telegram-safe HTML
        html = self._markdown_to_telegram_html(content)

        last_error = None
        for attempt in range(max_retries):
            try:
                await self._app.bot.send_message(
                    chat_id=int(chat_id),
                    text=html,
                    parse_mode="HTML",
                )
                return True
            except Exception as e:
                last_error = e
                if attempt < max_retries - 1:
                    wait_time = min(2**attempt, 10)  # Exponential backoff, max 10s
                    logger.warning(
                        f"Telegram send failed (attempt {attempt + 1}/{max_retries}): {e}. Retrying in {wait_time}s..."
                    )
                    await asyncio.sleep(wait_time)
                else:
                    # Final attempt failed, try fallback
                    break

        # Fallback to plain text
        for attempt in range(2):
            try:
                await self._app.bot.send_message(
                    chat_id=int(chat_id),
                    text=content,
                )
                return True
            except Exception as e:
                last_error = e
                if attempt == 0:
                    await asyncio.sleep(1)

        logger.error(f"Failed to send Telegram message after all retries: {last_error}")
        return False

    async def _handle_outbound(self, message: OutboundMessage) -> None:
        """Handle outbound messages directed to Telegram."""
        if message.channel == "telegram":
            await self.send(message.channel_id, message.content)

    async def _cmd_start(self, update: Any, context: Any) -> None:
        """Handle /start command."""
        await self._process_command(update, "/start")

    async def _cmd_new(self, update: Any, context: Any) -> None:
        """Handle /new command."""
        await self._process_command(update, "/new")

    async def _cmd_help(self, update: Any, context: Any) -> None:
        """Handle /help command."""
        await self._process_command(update, "/help")

    async def _cmd_status(self, update: Any, context: Any) -> None:
        """Handle /status command."""
        await self._process_command(update, "/status")

    async def _process_command(self, update: Any, command: str) -> None:
        """Process a slash command."""
        if not update.effective_message or not update.effective_user:
            return

        user = update.effective_user
        if not self.is_allowed(str(user.id)):
            return

        chat_id = update.effective_chat.id

        await self._bus.publish_inbound(
            InboundMessage(
                channel="telegram",
                channel_id=f"telegram:{chat_id}",
                sender_id=str(user.id),
                sender_name=user.first_name or str(user.id),
                content=command,
            )
        )

    async def _on_message(self, update: Any, context: Any) -> None:
        """Handle incoming messages."""
        if not update.effective_message or not update.effective_user:
            return

        user = update.effective_user
        if not self.is_allowed(str(user.id)):
            return

        msg = update.effective_message
        chat_id = update.effective_chat.id
        content = msg.text or ""
        attachments: list[dict[str, Any]] = []

        # Handle photos
        if msg.photo:
            try:
                photo = msg.photo[-1]  # Largest size
                file = await photo.get_file()
                path = self._media_dir / f"photo_{chat_id}_{file.file_id}.jpg"
                await file.download_to_drive(str(path))
                attachments.append(
                    {"type": "image", "path": str(path), "url": f"file://{path}"}
                )
                if not content:
                    content = "(photo)"
            except Exception as e:
                logger.error(f"Failed to download photo: {e}")

        # Handle voice/audio
        if msg.voice or msg.audio:
            try:
                voice = msg.voice or msg.audio
                file = await voice.get_file()
                ext = "ogg" if msg.voice else "mp3"
                path = self._media_dir / f"audio_{chat_id}_{file.file_id}.{ext}"
                await file.download_to_drive(str(path))

                # Transcribe if transcriber available
                if self._transcriber:
                    transcription = await self._transcriber.transcribe(str(path))
                    content = (
                        f"{content}\n[Voice transcription: {transcription}]"
                        if content
                        else f"[Voice: {transcription}]"
                    )
                else:
                    content = (
                        f"{content}\n[Voice message saved to {path}]"
                        if content
                        else f"[Voice message saved to {path}]"
                    )
            except Exception as e:
                logger.error(f"Failed to process audio: {e}")

        # Handle documents
        if msg.document:
            try:
                file = await msg.document.get_file()
                path = (
                    self._media_dir
                    / f"doc_{chat_id}_{msg.document.file_name or file.file_id}"
                )
                await file.download_to_drive(str(path))
                attachments.append(
                    {
                        "type": "document",
                        "path": str(path),
                        "name": msg.document.file_name,
                    }
                )
                if not content:
                    content = f"(document: {msg.document.file_name})"
            except Exception as e:
                logger.error(f"Failed to download document: {e}")

        if not content:
            return

        # Start typing indicator
        self._start_typing(chat_id)

        # Publish to bus
        await self._bus.publish_inbound(
            InboundMessage(
                channel="telegram",
                channel_id=f"telegram:{chat_id}",
                sender_id=str(user.id),
                sender_name=user.first_name or str(user.id),
                content=content,
                attachments=attachments,
            )
        )

    def _start_typing(self, chat_id: int) -> None:
        """Start sending typing indicator every 4 seconds."""
        self._stop_typing(chat_id)
        self._typing_tasks[chat_id] = asyncio.create_task(self._typing_loop(chat_id))

    def _stop_typing(self, chat_id: int) -> None:
        """Stop the typing indicator for a chat."""
        task = self._typing_tasks.pop(chat_id, None)
        if task:
            task.cancel()

    async def _typing_loop(self, chat_id: int) -> None:
        """Send typing action every 4 seconds."""
        try:
            while True:
                try:
                    await self._app.bot.send_chat_action(
                        chat_id=chat_id, action="typing"
                    )
                except Exception:
                    pass
                await asyncio.sleep(4)
        except asyncio.CancelledError:
            pass

    @staticmethod
    def _markdown_to_telegram_html(text: str) -> str:
        """Convert markdown to Telegram-safe HTML.

        Telegram supports a limited subset of HTML:
        <b>, <i>, <u>, <s>, <code>, <pre>, <a href="">
        """
        import html

        # Escape HTML entities first
        result = html.escape(text)

        # Code blocks: ```lang\n...\n``` -> <pre><code>...</code></pre>
        result = re.sub(
            r"```(\w*)\n(.*?)```",
            lambda m: f'<pre><code class="language-{m.group(1)}">{m.group(2)}</code></pre>'
            if m.group(1)
            else f"<pre>{m.group(2)}</pre>",
            result,
            flags=re.DOTALL,
        )

        # Inline code: `...` -> <code>...</code>
        result = re.sub(r"`([^`]+)`", r"<code>\1</code>", result)

        # Bold: **text** or __text__ -> <b>text</b>
        result = re.sub(r"\*\*(.+?)\*\*", r"<b>\1</b>", result)
        result = re.sub(r"__(.+?)__", r"<b>\1</b>", result)

        # Italic: *text* or _text_ -> <i>text</i>
        result = re.sub(r"(?<!\*)\*(?!\*)(.+?)(?<!\*)\*(?!\*)", r"<i>\1</i>", result)
        result = re.sub(r"(?<!_)_(?!_)(.+?)(?<!_)_(?!_)", r"<i>\1</i>", result)

        # Strikethrough: ~~text~~ -> <s>text</s>
        result = re.sub(r"~~(.+?)~~", r"<s>\1</s>", result)

        # Links: [text](url) -> <a href="url">text</a>
        result = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", r'<a href="\2">\1</a>', result)

        # Headers: strip # markers, make bold
        result = re.sub(r"^#{1,6}\s+(.+)$", r"<b>\1</b>", result, flags=re.MULTILINE)

        # Bullet lists: - item -> * item
        result = re.sub(r"^[-*]\s+", "- ", result, flags=re.MULTILINE)

        return result
