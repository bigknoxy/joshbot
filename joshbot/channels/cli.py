"""Interactive CLI channel using prompt-toolkit and rich."""

from __future__ import annotations

import asyncio
from typing import Any

from loguru import logger

from ..bus.events import InboundMessage, OutboundMessage
from ..bus.queue import MessageBus
from .base import BaseChannel


class CLIChannel(BaseChannel):
    """Interactive terminal channel with rich markdown output."""

    def __init__(self, bus: MessageBus) -> None:
        super().__init__(bus)
        self._running = False
        self._response_event = asyncio.Event()
        self._pending_response: str = ""

    @property
    def name(self) -> str:
        return "cli"

    async def start(self) -> None:
        """Start the CLI input loop."""
        self._running = True

        # Register for outbound messages
        self._bus.on_outbound(self._handle_outbound)

        # Run input loop
        await self._input_loop()

    async def stop(self) -> None:
        """Stop the CLI channel."""
        self._running = False

    async def send(self, channel_id: str, content: str) -> None:
        """Send a message to the CLI (print it)."""
        self._print_response(content)

    async def _handle_outbound(self, message: OutboundMessage) -> None:
        """Handle outbound messages directed to CLI."""
        if message.channel == "cli":
            self._pending_response = message.content
            self._response_event.set()

    async def _input_loop(self) -> None:
        """Main input loop using prompt-toolkit."""
        try:
            from prompt_toolkit import PromptSession
            from prompt_toolkit.history import FileHistory
            from pathlib import Path

            history_dir = Path.home() / ".joshbot" / "history"
            history_dir.mkdir(parents=True, exist_ok=True)
            history_file = history_dir / "cli_history"

            session = PromptSession(
                history=FileHistory(str(history_file)),
                multiline=False,
            )

            self._print_welcome()

            while self._running:
                try:
                    # Get input (run in thread to not block event loop)
                    user_input = await asyncio.get_event_loop().run_in_executor(
                        None,
                        lambda: session.prompt("\nyou> "),
                    )

                    if not user_input.strip():
                        continue

                    if user_input.strip().lower() in ("exit", "quit", "bye"):
                        self._print_message("Goodbye! 👋")
                        self._running = False
                        break

                    # Publish to bus
                    self._response_event.clear()
                    await self._bus.publish_inbound(
                        InboundMessage(
                            channel="cli",
                            channel_id="cli:local",
                            sender_id="local",
                            sender_name="User",
                            content=user_input,
                        )
                    )

                    # Wait for response
                    self._print_thinking()
                    try:
                        await asyncio.wait_for(self._response_event.wait(), timeout=300)
                    except asyncio.TimeoutError:
                        self._print_message("(Response timed out)")

                except (EOFError, KeyboardInterrupt):
                    self._print_message("\nGoodbye!")
                    self._running = False
                    break

        except ImportError:
            logger.warning("prompt-toolkit not available, using basic input")
            await self._basic_input_loop()

    async def _basic_input_loop(self) -> None:
        """Fallback input loop without prompt-toolkit."""
        self._print_welcome()

        while self._running:
            try:
                user_input = await asyncio.get_event_loop().run_in_executor(
                    None,
                    lambda: input("\nyou> "),
                )

                if not user_input.strip():
                    continue

                if user_input.strip().lower() in ("exit", "quit", "bye"):
                    print("Goodbye!")
                    break

                self._response_event.clear()
                await self._bus.publish_inbound(
                    InboundMessage(
                        channel="cli",
                        channel_id="cli:local",
                        sender_id="local",
                        sender_name="User",
                        content=user_input,
                    )
                )

                print("\n⏳ Thinking...")
                try:
                    await asyncio.wait_for(self._response_event.wait(), timeout=300)
                except asyncio.TimeoutError:
                    print("(Response timed out)")

            except (EOFError, KeyboardInterrupt):
                print("\nGoodbye!")
                break

    def _print_welcome(self) -> None:
        """Print welcome message."""
        try:
            from rich.console import Console
            from rich.panel import Panel

            console = Console()
            console.print(
                Panel.fit(
                    "[bold blue]joshbot[/bold blue] - Your Personal AI Assistant\n"
                    "Type [bold]exit[/bold] to quit, [bold]/help[/bold] for commands",
                    title="Welcome",
                    border_style="blue",
                )
            )
        except ImportError:
            print("=" * 50)
            print("  joshbot - Your Personal AI Assistant")
            print("  Type 'exit' to quit, '/help' for commands")
            print("=" * 50)

    def _print_response(self, content: str) -> None:
        """Print a response with rich markdown formatting."""
        try:
            from rich.console import Console
            from rich.markdown import Markdown
            from rich.panel import Panel

            console = Console()
            console.print()
            console.print(
                Panel(
                    Markdown(content),
                    title="[bold green]joshbot[/bold green]",
                    border_style="green",
                    padding=(1, 2),
                )
            )
        except ImportError:
            print(f"\njoshbot> {content}")

    def _print_thinking(self) -> None:
        """Print thinking indicator."""
        try:
            from rich.console import Console

            console = Console()
            console.print("\n⏳ [dim]Thinking...[/dim]")
        except ImportError:
            print("\n⏳ Thinking...")

    def _print_message(self, message: str) -> None:
        """Print a system message."""
        try:
            from rich.console import Console

            console = Console()
            console.print(f"[dim]{message}[/dim]")
        except ImportError:
            print(message)
