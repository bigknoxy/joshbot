"""Web tools: search and fetch."""

from __future__ import annotations

from typing import Any

from loguru import logger

from .base import Tool


class WebSearchTool(Tool):
    """Search the web using Brave Search API."""

    def __init__(self, api_key: str = ""):
        self._api_key = api_key

    @property
    def name(self) -> str:
        return "web_search"

    @property
    def description(self) -> str:
        return "Search the web for information. Returns titles, URLs, and snippets from search results."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "Search query"},
                "count": {
                    "type": "integer",
                    "description": "Number of results (default: 5, max: 10)",
                    "default": 5,
                },
            },
            "required": ["query"],
        }

    async def execute(self, query: str, count: int = 5, **kwargs: Any) -> str:
        if not self._api_key:
            return "Error: Web search is not configured. Set tools.web.search.api_key in config."

        import httpx

        try:
            async with httpx.AsyncClient() as client:
                response = await client.get(
                    "https://api.search.brave.com/res/v1/web/search",
                    headers={
                        "Accept": "application/json",
                        "Accept-Encoding": "gzip",
                        "X-Subscription-Token": self._api_key,
                    },
                    params={"q": query, "count": min(count, 10)},
                    timeout=15.0,
                )
                response.raise_for_status()
                data = response.json()

            results = data.get("web", {}).get("results", [])
            if not results:
                return f"No results found for: {query}"

            lines = []
            for i, r in enumerate(results, 1):
                title = r.get("title", "Untitled")
                url = r.get("url", "")
                snippet = r.get("description", "No description")
                lines.append(f"{i}. {title}\n   {url}\n   {snippet}\n")

            return "\n".join(lines)
        except Exception as e:
            return f"Error searching: {e}"


class WebFetchTool(Tool):
    """Fetch and extract readable content from a URL."""

    @property
    def name(self) -> str:
        return "web_fetch"

    @property
    def description(self) -> str:
        return "Fetch a URL and extract its readable text content. Good for reading articles and documentation."

    @property
    def parameters(self) -> dict[str, Any]:
        return {
            "type": "object",
            "properties": {
                "url": {"type": "string", "description": "URL to fetch"},
                "max_length": {
                    "type": "integer",
                    "description": "Max content length in chars (default: 50000)",
                    "default": 50000,
                },
            },
            "required": ["url"],
        }

    async def execute(self, url: str, max_length: int = 50000, **kwargs: Any) -> str:
        import httpx

        # Validate URL
        if not url.startswith(("http://", "https://")):
            return "Error: URL must start with http:// or https://"

        try:
            async with httpx.AsyncClient(
                follow_redirects=True, max_redirects=5
            ) as client:
                response = await client.get(
                    url,
                    headers={"User-Agent": "Mozilla/5.0 (compatible; joshbot/0.1)"},
                    timeout=20.0,
                )
                response.raise_for_status()
                html = response.text

            # Try readability extraction
            try:
                from readability import Document

                doc = Document(html)
                title = doc.title()

                # Extract text from HTML
                from lxml import etree
                from lxml.html import fromstring

                clean_html = doc.summary()
                tree = fromstring(clean_html)
                text = tree.text_content().strip()

                result = f"Title: {title}\nURL: {url}\n\n{text}"
            except Exception:
                # Fallback: strip HTML tags
                import re

                text = re.sub(r"<[^>]+>", " ", html)
                text = re.sub(r"\s+", " ", text).strip()
                result = f"URL: {url}\n\n{text}"

            if len(result) > max_length:
                result = (
                    result[:max_length]
                    + f"\n\n... (truncated, {len(result) - max_length} more chars)"
                )

            return result
        except Exception as e:
            return f"Error fetching URL: {e}"
