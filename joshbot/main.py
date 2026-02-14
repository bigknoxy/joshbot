"""Joshbot CLI entry point."""

from __future__ import annotations

import asyncio
import json
import sys
from pathlib import Path
from typing import Optional

import typer
from loguru import logger
from rich.console import Console
from rich.panel import Panel
from rich.prompt import Prompt, Confirm

# Configure loguru to use WARNING level by default (not DEBUG)
logger.remove()
logger.add(sys.stderr, level="WARNING")

app = typer.Typer(
    name="joshbot",
    help="A lightweight personal AI assistant with self-learning and long-term memory.",
    no_args_is_help=True,
)

console = Console()


def _get_bundled_skills_dir() -> Path:
    """Get the bundled skills directory (relative to package)."""
    return Path(__file__).parent.parent / "skills"


def _build_tools(config, bus, cron_service):
    """Build and register all tools."""
    from .tools.registry import ToolRegistry
    from .tools.filesystem import ReadFileTool, WriteFileTool, EditFileTool, ListDirTool
    from .tools.shell import ShellTool
    from .tools.web import WebSearchTool, WebFetchTool
    from .tools.message import MessageTool
    from .tools.spawn import SpawnTool
    from .tools.cron import CronTool

    workspace = config.agents.defaults.workspace
    restrict = config.tools.restrict_to_workspace

    registry = ToolRegistry()
    registry.register(ReadFileTool(workspace=workspace, restrict=restrict))
    registry.register(WriteFileTool(workspace=workspace, restrict=restrict))
    registry.register(EditFileTool(workspace=workspace, restrict=restrict))
    registry.register(ListDirTool(workspace=workspace, restrict=restrict))
    registry.register(
        ShellTool(
            timeout=config.tools.exec.timeout,
            workspace=workspace,
            restrict=restrict,
        )
    )
    registry.register(WebSearchTool(api_key=config.tools.web.search.api_key))
    registry.register(WebFetchTool())

    msg_tool = MessageTool()
    msg_tool.set_bus(bus)
    registry.register(msg_tool)

    registry.register(SpawnTool())

    cron_tool = CronTool()
    cron_tool.set_cron_service(cron_service)
    registry.register(cron_tool)

    return registry


async def _run_agent_mode(config):
    """Run joshbot in interactive CLI mode."""
    from .bus.queue import MessageBus
    from .providers.litellm_provider import LiteLLMProvider
    from .agent.loop import AgentLoop
    from .agent.memory import MemoryManager
    from .agent.skills import SkillsLoader
    from .session.manager import SessionManager
    from .channels.cli import CLIChannel
    from .cron.service import CronService
    from .config.loader import ensure_dirs

    ensure_dirs(config)

    # Initialize components
    bus = MessageBus()
    provider = LiteLLMProvider(config)
    session_manager = SessionManager(config.sessions_dir)
    memory = MemoryManager(config.agents.defaults.workspace)
    await memory.initialize()

    skills = SkillsLoader(
        bundled_dir=_get_bundled_skills_dir(),
        workspace_dir=config.agents.defaults.workspace,
    )
    skills.discover()

    cron_service = CronService(config.cron_dir, bus)
    tools = _build_tools(config, bus, cron_service)

    # Create agent loop
    agent = AgentLoop(
        config=config,
        provider=provider,
        tools=tools,
        bus=bus,
        session_manager=session_manager,
        memory_context_fn=memory.get_memory_context,
        skills_summary_fn=lambda: asyncio.coroutine(lambda: skills.get_summary())()
        if False
        else asyncio.sleep(0, result=skills.get_summary()),
    )

    # Create CLI channel
    cli = CLIChannel(bus)

    # Start everything
    await cron_service.start()

    # Run bus and CLI concurrently
    bus_task = asyncio.create_task(bus.start())

    try:
        await cli.start()
    except (KeyboardInterrupt, EOFError):
        pass
    finally:
        await bus.stop()
        await cron_service.stop()


async def _run_gateway_mode(config):
    """Run joshbot in gateway mode (Telegram + all channels)."""
    from .bus.queue import MessageBus
    from .providers.litellm_provider import LiteLLMProvider
    from .agent.loop import AgentLoop
    from .agent.memory import MemoryManager
    from .agent.skills import SkillsLoader
    from .session.manager import SessionManager
    from .channels.manager import ChannelManager
    from .cron.service import CronService
    from .heartbeat.service import HeartbeatService
    from .config.loader import ensure_dirs

    ensure_dirs(config)

    # Initialize components
    bus = MessageBus()
    provider = LiteLLMProvider(config)
    session_manager = SessionManager(config.sessions_dir)
    memory = MemoryManager(config.agents.defaults.workspace)
    await memory.initialize()

    skills = SkillsLoader(
        bundled_dir=_get_bundled_skills_dir(),
        workspace_dir=config.agents.defaults.workspace,
    )
    skills.discover()

    cron_service = CronService(config.cron_dir, bus)
    tools = _build_tools(config, bus, cron_service)

    # Create agent loop
    agent = AgentLoop(
        config=config,
        provider=provider,
        tools=tools,
        bus=bus,
        session_manager=session_manager,
        memory_context_fn=memory.get_memory_context,
        skills_summary_fn=lambda: asyncio.sleep(0, result=skills.get_summary()),
    )

    # Setup channels
    channel_manager = ChannelManager(config, bus)
    channel_manager.setup_channels(transcriber=provider)

    # Setup heartbeat
    heartbeat = HeartbeatService(
        workspace=config.agents.defaults.workspace,
        bus=bus,
    )
    await heartbeat.initialize()

    console.print(
        Panel.fit(
            "[bold blue]joshbot gateway[/bold blue] is running\n"
            f"Active channels: {', '.join(channel_manager.active_channels) or 'none'}\n"
            f"Model: {config.agents.defaults.model}\n"
            f"Tools: {len(tools)} registered\n"
            "Press Ctrl+C to stop",
            title="Gateway Mode",
            border_style="blue",
        )
    )

    # Start all services
    try:
        await asyncio.gather(
            bus.start(),
            channel_manager.start_all(),
            cron_service.start(),
            heartbeat.start(),
        )
    except (KeyboardInterrupt, asyncio.CancelledError):
        pass
    finally:
        await heartbeat.stop()
        await cron_service.stop()
        await channel_manager.stop_all()
        await bus.stop()
        console.print("[yellow]Gateway stopped.[/yellow]")


# Personality templates
PERSONALITIES = {
    "professional": {
        "name": "Professional",
        "description": "Concise, task-focused, minimal small talk",
        "soul": """# Soul

## Personality
I am professional, efficient, and focused. I communicate clearly and concisely.
I prioritize getting things done over making conversation.

## Communication Style
- Direct and to-the-point
- Use technical language when appropriate
- Avoid unnecessary pleasantries
- Focus on actionable information

## Values
- Accuracy and correctness
- Efficiency and productivity
- Clear communication
""",
    },
    "friendly": {
        "name": "Friendly",
        "description": "Warm, conversational, uses humor",
        "soul": """# Soul

## Personality
I am warm, approachable, and genuinely interested in helping. I enjoy conversation
and like to add a bit of humor when appropriate.

## Communication Style
- Friendly and conversational
- Use analogies and examples to explain things
- Light humor to keep things engaging
- Encouraging and supportive

## Values
- Helpfulness and empathy
- Making complex things accessible
- Building rapport
- Positive energy
""",
    },
    "sarcastic": {
        "name": "Sarcastic",
        "description": "Witty, dry humor, still helpful underneath",
        "soul": """# Soul

## Personality
I have a sharp wit and a dry sense of humor. I'm the friend who roasts you
but always has your back. Underneath the sarcasm, I'm deeply helpful.

## Communication Style
- Dry wit and clever observations
- Self-deprecating humor
- Still accurate and helpful with actual advice
- Never mean-spirited, always playful

## Values
- Honesty wrapped in humor
- Getting to the truth
- Not taking things too seriously
- Being genuinely helpful despite the snark
""",
    },
    "minimal": {
        "name": "Minimal",
        "description": "Extremely terse, just the facts",
        "soul": """# Soul

## Personality
Maximum information, minimum words.

## Communication Style
- Brief
- No filler
- Code > prose
- Facts only

## Values
- Brevity
- Precision
- Efficiency
""",
    },
}


@app.command()
def onboard():
    """Set up joshbot for the first time."""
    from .config.schema import Config, ProviderConfig, DEFAULT_HOME
    from .config.loader import save_config, ensure_dirs

    console.print(
        Panel.fit(
            "[bold blue]Welcome to joshbot![/bold blue]\n\n"
            "Let's get you set up. This will create your configuration\n"
            "and workspace files.",
            title="Onboarding",
            border_style="blue",
        )
    )

    # Check if already configured
    config_file = DEFAULT_HOME / "config.json"
    if config_file.exists():
        if not Confirm.ask("Configuration already exists. Overwrite?", default=False):
            console.print("[yellow]Onboarding cancelled.[/yellow]")
            raise typer.Exit()

    # Get API key
    console.print("\n[bold]Step 1: LLM Provider[/bold]")
    console.print(
        "joshbot uses OpenRouter by default (supports many models with one API key)."
    )
    console.print(
        "Get a free key at: [link=https://openrouter.ai/keys]https://openrouter.ai/keys[/link]\n"
    )

    api_key = Prompt.ask("Enter your OpenRouter API key (or press Enter to skip)")

    # Choose personality
    console.print("\n[bold]Step 2: Personality[/bold]")
    console.print("Choose joshbot's personality:\n")

    for i, (key, p) in enumerate(PERSONALITIES.items(), 1):
        console.print(f"  {i}. [bold]{p['name']}[/bold] - {p['description']}")
    console.print(f"  5. [bold]Custom[/bold] - Write your own SOUL.md")

    choice = Prompt.ask(
        "\nChoose personality", choices=["1", "2", "3", "4", "5"], default="2"
    )

    personality_keys = list(PERSONALITIES.keys())
    if choice in ("1", "2", "3", "4"):
        selected = personality_keys[int(choice) - 1]
        soul_content = PERSONALITIES[selected]["soul"]
        console.print(f"Selected: [bold]{PERSONALITIES[selected]['name']}[/bold]")
    else:
        soul_content = "# Soul\n\n## Personality\n(Write your personality here)\n\n## Communication Style\n(Describe your preferred style)\n"
        console.print("You can edit SOUL.md later in your workspace.")

    # Choose model
    console.print("\n[bold]Step 3: Model[/bold]")
    console.print("Default model: google/gemma-2-9b-it:free (free via OpenRouter)")
    console.print("You can change this later in ~/.joshbot/config.json\n")

    model = Prompt.ask(
        "Model name",
        default="google/gemma-2-9b-it:free",
    )

    # Build config
    config = Config(
        providers={"openrouter": ProviderConfig(api_key=api_key)} if api_key else {},
        agents={"defaults": {"model": model}},
    )

    # Save config and create workspace
    ensure_dirs(config)
    save_config(config)

    # Write workspace files
    ws = config.workspace_dir

    # SOUL.md
    (ws / "SOUL.md").write_text(soul_content, encoding="utf-8")

    # USER.md
    user_content = """# User Profile

## About You
- Name: (your name)
- Location: (your location)
- Timezone: (your timezone)

## Preferences
- (add your preferences here)

## Current Projects
- (what are you working on?)

## Notes
- (anything else joshbot should know)
"""
    (ws / "USER.md").write_text(user_content, encoding="utf-8")

    # AGENTS.md
    agents_content = """# Agent Instructions

## General Guidelines
- Be helpful and proactive
- Use tools to verify information when possible
- Keep responses appropriately detailed
- Remember context from previous conversations using the memory system
- Create skills when you learn new capabilities

## Tool Usage
- Prefer reading files before editing them
- Use shell commands carefully (safety guards are active)
- Search the web when you need current information
- Update memory when you learn something important about the user
"""
    (ws / "AGENTS.md").write_text(agents_content, encoding="utf-8")

    # IDENTITY.md
    identity_content = """# Identity

I am joshbot, a personal AI assistant.
I am always learning and improving through conversations.
I remember important information across sessions.
I can create new skills to extend my capabilities.
"""
    (ws / "IDENTITY.md").write_text(identity_content, encoding="utf-8")

    # Initialize memory files
    from .agent.memory import MemoryManager
    import asyncio

    memory = MemoryManager(str(ws))
    asyncio.get_event_loop().run_until_complete(memory.initialize())

    # Initialize heartbeat
    from .heartbeat.service import HeartbeatService
    from .bus.queue import MessageBus

    bus = MessageBus()
    heartbeat = HeartbeatService(str(ws), bus)
    asyncio.get_event_loop().run_until_complete(heartbeat.initialize())

    console.print(
        Panel.fit(
            "[bold green]Setup complete![/bold green]\n\n"
            f"Config: {DEFAULT_HOME / 'config.json'}\n"
            f"Workspace: {ws}\n\n"
            "Quick start:\n"
            "  [bold]joshbot agent[/bold]    - Chat in the terminal\n"
            "  [bold]joshbot gateway[/bold]  - Start Telegram + all channels\n"
            "  [bold]joshbot status[/bold]   - Check configuration\n\n"
            "Edit ~/.joshbot/config.json to configure Telegram and other settings.",
            title="Ready!",
            border_style="green",
        )
    )


@app.command()
def agent():
    """Start joshbot in interactive CLI mode."""
    from .config.loader import load_config

    config = load_config()

    if not config.providers:
        console.print(
            "[red]No providers configured. Run 'joshbot onboard' first.[/red]"
        )
        raise typer.Exit(1)

    try:
        asyncio.run(_run_agent_mode(config))
    except KeyboardInterrupt:
        console.print("\n[yellow]Goodbye![/yellow]")


@app.command()
def gateway():
    """Start joshbot in gateway mode (Telegram + all channels)."""
    from .config.loader import load_config

    config = load_config()

    if not config.providers:
        console.print(
            "[red]No providers configured. Run 'joshbot onboard' first.[/red]"
        )
        raise typer.Exit(1)

    try:
        asyncio.run(_run_gateway_mode(config))
    except KeyboardInterrupt:
        console.print("\n[yellow]Gateway stopped.[/yellow]")


@app.command()
def status():
    """Show joshbot configuration and status."""
    from .config.loader import load_config, CONFIG_FILE
    from .config.schema import DEFAULT_HOME

    config = load_config()

    # Config file
    config_exists = CONFIG_FILE.exists()
    ws_exists = config.workspace_dir.exists()

    console.print(
        Panel.fit(
            f"[bold]Config file:[/bold] {CONFIG_FILE} {'[green](exists)[/green]' if config_exists else '[red](missing)[/red]'}\n"
            f"[bold]Workspace:[/bold] {config.workspace_dir} {'[green](exists)[/green]' if ws_exists else '[red](missing)[/red]'}\n"
            f"[bold]Sessions:[/bold] {config.sessions_dir}\n"
            f"\n[bold]Model:[/bold] {config.agents.defaults.model}\n"
            f"[bold]Max tokens:[/bold] {config.agents.defaults.max_tokens}\n"
            f"[bold]Temperature:[/bold] {config.agents.defaults.temperature}\n"
            f"[bold]Memory window:[/bold] {config.agents.defaults.memory_window}\n"
            f"\n[bold]Providers:[/bold] {', '.join(config.providers.keys()) if config.providers else 'none'}\n"
            f"[bold]Telegram:[/bold] {'enabled' if config.channels.telegram.enabled else 'disabled'}\n"
            f"[bold]Workspace restricted:[/bold] {'yes' if config.tools.restrict_to_workspace else 'no'}",
            title="joshbot status",
            border_style="blue",
        )
    )

    # Check memory files
    memory_file = config.workspace_dir / "memory" / "MEMORY.md"
    history_file = config.workspace_dir / "memory" / "HISTORY.md"

    if memory_file.exists():
        size = memory_file.stat().st_size
        console.print(f"  MEMORY.md: {size} bytes")
    if history_file.exists():
        size = history_file.stat().st_size
        lines = len(history_file.read_text().splitlines())
        console.print(f"  HISTORY.md: {size} bytes, {lines} lines")


if __name__ == "__main__":
    app()
