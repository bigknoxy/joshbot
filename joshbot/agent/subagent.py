"""Background subagent spawner for isolated task execution."""

from __future__ import annotations

import asyncio
import json
from typing import Any

from loguru import logger

from ..providers.base import LLMProvider
from ..tools.registry import ToolRegistry


class SubagentRunner:
    """Run isolated agent loops for background tasks.

    Subagents have access to tools but CANNOT:
    - Send messages (no message tool)
    - Spawn sub-tasks (no spawn tool)
    This prevents runaway nested spawning.
    """

    def __init__(
        self,
        provider: LLMProvider,
        tools: ToolRegistry,
        model: str = "",
        max_tokens: int = 8192,
        temperature: float = 0.7,
        max_iterations: int = 10,
    ) -> None:
        self._provider = provider
        self._tools = tools
        self._model = model
        self._max_tokens = max_tokens
        self._temperature = temperature
        self._max_iterations = max_iterations

    async def run(self, task: str, context: str = "") -> str:
        """Run a subagent loop for a given task.

        Returns the final text response from the subagent.
        """
        logger.info(f"Subagent started: {task[:80]}")

        # Build restricted tool schemas (no message, no spawn)
        restricted_schemas = [
            schema
            for schema in self._tools.get_schemas()
            if schema["function"]["name"] not in ("message", "spawn")
        ]

        system_prompt = (
            "You are a background task runner for joshbot. "
            "Complete the assigned task using available tools. "
            "Be thorough but efficient. When done, provide a clear summary of results."
        )

        user_content = f"Task: {task}"
        if context:
            user_content += f"\n\nAdditional context: {context}"

        messages: list[dict[str, Any]] = [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_content},
        ]

        for iteration in range(self._max_iterations):
            try:
                response = await self._provider.chat(
                    messages=messages,
                    tools=restricted_schemas if restricted_schemas else None,
                    model=self._model,
                    max_tokens=self._max_tokens,
                    temperature=self._temperature,
                )

                if not response.has_tool_calls:
                    logger.info(f"Subagent completed in {iteration + 1} iterations")
                    return response.content or "(no output)"

                # Execute tool calls
                tool_calls_data = []
                for tc in response.tool_calls:
                    # Skip restricted tools
                    if tc.name in ("message", "spawn"):
                        continue
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

                if tool_calls_data:
                    messages.append(
                        {
                            "role": "assistant",
                            "content": response.content or None,
                            "tool_calls": tool_calls_data,
                        }
                    )

                    for tc in response.tool_calls:
                        if tc.name in ("message", "spawn"):
                            continue
                        result = await self._tools.execute(tc.name, tc.arguments)
                        messages.append(
                            {
                                "role": "tool",
                                "tool_call_id": tc.id,
                                "name": tc.name,
                                "content": result,
                            }
                        )
                else:
                    return response.content or "(no output)"

            except Exception as e:
                logger.error(f"Subagent iteration {iteration + 1} failed: {e}")
                return f"Subagent error: {e}"

        return "Subagent reached maximum iterations. Partial results may be available."
