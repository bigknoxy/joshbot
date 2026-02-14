"""Core agent loop implementing the ReAct pattern."""

from __future__ import annotations

import asyncio
import json
from pathlib import Path
from typing import Any

from loguru import logger

from ..bus.events import InboundMessage, OutboundMessage
from ..bus.queue import MessageBus
from ..config.schema import Config
from ..providers.base import LLMProvider, LLMResponse
from ..session.manager import SessionManager
from ..tools.registry import ToolRegistry
from .context import build_system_prompt, format_tool_result


class AgentLoop:
    """Main agent loop: receive messages, process with LLM + tools, respond."""

    def __init__(
        self,
        config: Config,
        provider: LLMProvider,
        tools: ToolRegistry,
        bus: MessageBus,
        session_manager: SessionManager,
        memory_context_fn: Any = None,
        skills_summary_fn: Any = None,
    ) -> None:
        self._config = config
        self._provider = provider
        self._tools = tools
        self._bus = bus
        self._sessions = session_manager
        self._memory_context_fn = memory_context_fn
        self._skills_summary_fn = skills_summary_fn

        # Register as inbound handler
        self._bus.on_inbound(self._process_message)

    async def _process_message(self, message: InboundMessage) -> None:
        """Process an inbound message through the agent loop."""
        logger.info(
            f"Processing message from {message.sender_name} on {message.channel}"
        )

        try:
            # Handle commands
            if message.content.startswith("/"):
                response = await self._handle_command(message)
                if response:
                    await self._send_response(message, response)
                    return

            # Get or create session
            session = await self._sessions.get_or_create(message.session_key)

            # Check if memory consolidation is needed
            defaults = self._config.agents.defaults
            if len(session.messages) > defaults.memory_window:
                await self._consolidate_memory(session)

            # Build context
            memory_context = ""
            if self._memory_context_fn:
                memory_context = await self._memory_context_fn()

            skills_summary = ""
            if self._skills_summary_fn:
                skills_summary = await self._skills_summary_fn()

            system_prompt = build_system_prompt(
                workspace=defaults.workspace,
                skills_summary=skills_summary,
                memory_context=memory_context,
            )

            # Add user message to session
            user_msg = {"role": "user", "content": message.content}
            if message.attachments:
                # Handle image attachments
                content_parts: list[dict[str, Any]] = [
                    {"type": "text", "text": message.content}
                ]
                for att in message.attachments:
                    if att.get("type") == "image":
                        content_parts.append(
                            {
                                "type": "image_url",
                                "image_url": {"url": att["url"]},
                            }
                        )
                user_msg = {"role": "user", "content": content_parts}

            session.messages.append(user_msg)

            # Build messages for LLM
            messages = [{"role": "system", "content": system_prompt}] + session.messages

            # Run the ReAct loop
            response_content = await self._react_loop(messages, session)

            # Save session
            await self._sessions.save(session)

            # Send response
            await self._send_response(message, response_content)

        except Exception as e:
            logger.error(f"Error processing message: {e}")
            await self._send_response(message, f"Sorry, I encountered an error: {e}")

    async def _react_loop(self, messages: list[dict[str, Any]], session: Any) -> str:
        """Run the ReAct loop: LLM -> tools -> reflect -> repeat."""
        defaults = self._config.agents.defaults
        max_iterations = defaults.max_tool_iterations
        tool_schemas = self._tools.get_schemas()

        for iteration in range(max_iterations):
            logger.debug(f"ReAct iteration {iteration + 1}/{max_iterations}")

            # Call LLM
            response = await self._provider.chat(
                messages=messages,
                tools=tool_schemas if tool_schemas else None,
                model=defaults.model,
                max_tokens=defaults.max_tokens,
                temperature=defaults.temperature,
            )

            # If no tool calls, we're done
            if not response.has_tool_calls:
                content = response.content or ""
                session.messages.append({"role": "assistant", "content": content})
                return content

            # Build assistant message with tool calls
            tool_calls_data = []
            for tc in response.tool_calls:
                tool_calls_data.append(
                    {
                        "id": tc.id,
                        "type": "function",
                        "function": {
                            "name": tc.name,
                            "arguments": json.dumps(tc.arguments),
                        },
                    }
                )

            assistant_msg: dict[str, Any] = {
                "role": "assistant",
                "content": response.content or None,
                "tool_calls": tool_calls_data,
            }
            messages.append(assistant_msg)
            session.messages.append(assistant_msg)

            # Execute all tool calls
            for tc in response.tool_calls:
                logger.info(
                    f"Executing tool: {tc.name}({json.dumps(tc.arguments)[:100]})"
                )
                result = await self._tools.execute(tc.name, tc.arguments)

                tool_msg = format_tool_result(tc.id, tc.name, result)
                messages.append(tool_msg)
                session.messages.append(tool_msg)

            # Interleaved Chain-of-Thought: ask LLM to reflect
            if iteration < max_iterations - 1 and response.has_tool_calls:
                reflect_msg = {
                    "role": "user",
                    "content": "[System: Reflect on the tool results and decide your next action. If you have enough information to respond to the user, do so. Otherwise, use more tools.]",
                }
                messages.append(reflect_msg)
                # Don't save reflection prompts to session history

        # Hit max iterations
        logger.warning(f"Hit max iterations ({max_iterations})")
        return "I've been working on this for a while. Here's what I found so far - let me know if you'd like me to continue."

    async def _handle_command(self, message: InboundMessage) -> str | None:
        """Handle slash commands. Returns response or None."""
        cmd = message.content.strip().lower()

        if cmd == "/start":
            return "Hello! I'm joshbot, your personal AI assistant. How can I help you today?"
        elif cmd == "/new":
            # Clear session
            await self._sessions.delete(message.session_key)
            return (
                "Started a new conversation. Previous context has been saved to memory."
            )
        elif cmd == "/help":
            return (
                "Available commands:\n"
                "/start - Start a conversation\n"
                "/new - Start fresh (saves memory first)\n"
                "/help - Show this help\n"
                "/status - Show system status\n\n"
                "Just type normally to chat with me!"
            )
        elif cmd == "/status":
            session = await self._sessions.get_or_create(message.session_key)
            tool_count = len(self._tools)
            msg_count = len(session.messages)
            return (
                f"Status:\n"
                f"  Model: {self._config.agents.defaults.model}\n"
                f"  Tools: {tool_count} registered\n"
                f"  Session messages: {msg_count}\n"
                f"  Memory window: {self._config.agents.defaults.memory_window}\n"
            )

        return None  # Not a known command, process normally

    async def _consolidate_memory(self, session: Any) -> None:
        """Consolidate old messages into memory."""
        logger.info("Consolidating memory...")

        defaults = self._config.agents.defaults
        window = defaults.memory_window

        # Get messages to consolidate (older half)
        cutoff = window // 2
        old_messages = session.messages[:cutoff]

        if not old_messages:
            return

        # Format old messages for consolidation
        transcript = []
        for msg in old_messages:
            role = msg.get("role", "unknown")
            content = msg.get("content", "")
            if isinstance(content, list):
                content = " ".join(
                    p.get("text", "") for p in content if isinstance(p, dict)
                )
            if content:
                transcript.append(f"[{role}] {content[:500]}")

        consolidation_prompt = f"""Review this conversation transcript and extract two things:

1. MEMORY_UPDATE: Key facts about the user, their preferences, projects, decisions, and context that should be remembered long-term. Write as structured markdown.

2. HISTORY_ENTRY: A 2-5 sentence summary of what happened in this conversation, for the event log. Start with a timestamp.

Transcript:
{chr(10).join(transcript)}

Respond in this exact format:
---MEMORY_UPDATE---
(your memory update here)
---HISTORY_ENTRY---
(your history entry here)"""

        try:
            response = await self._provider.chat(
                messages=[{"role": "user", "content": consolidation_prompt}],
                model=defaults.model,
                max_tokens=2000,
                temperature=0.3,
            )

            content = response.content
            if "---MEMORY_UPDATE---" in content and "---HISTORY_ENTRY---" in content:
                parts = content.split("---HISTORY_ENTRY---")
                memory_update = parts[0].replace("---MEMORY_UPDATE---", "").strip()
                history_entry = parts[1].strip() if len(parts) > 1 else ""

                # Write memory update
                ws = Path(defaults.workspace)
                memory_file = ws / "memory" / "MEMORY.md"

                # Merge with existing memory
                existing = ""
                if memory_file.exists():
                    existing = memory_file.read_text()

                if memory_update:
                    memory_file.parent.mkdir(parents=True, exist_ok=True)
                    # Append new facts (LLM should handle dedup on next consolidation)
                    new_memory = (
                        f"{existing}\n\n{memory_update}".strip()
                        if existing
                        else memory_update
                    )
                    memory_file.write_text(new_memory)

                # Append history entry
                if history_entry:
                    from datetime import datetime, timezone

                    history_file = ws / "memory" / "HISTORY.md"
                    timestamp = datetime.now(timezone.utc).strftime("[%Y-%m-%d %H:%M]")
                    entry = f"\n{timestamp} {history_entry}\n"
                    with open(history_file, "a") as f:
                        f.write(entry)

            # Trim session to recent messages only
            session.messages = session.messages[cutoff:]
            logger.info(f"Memory consolidated. Trimmed {cutoff} old messages.")

        except Exception as e:
            logger.error(f"Memory consolidation failed: {e}")

    async def _send_response(self, original: InboundMessage, content: str) -> None:
        """Send a response back through the bus."""
        await self._bus.publish_outbound(
            OutboundMessage(
                channel=original.channel,
                channel_id=original.channel_id,
                content=content,
            )
        )
