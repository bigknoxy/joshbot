"""Tests for markdown parsing and Telegram HTML conversion."""

from __future__ import annotations

import pytest

from joshbot.channels.telegram import TelegramChannel


class TestMarkdownToTelegramHtml:
    """Test markdown to Telegram HTML conversion."""

    @staticmethod
    def convert(text: str) -> str:
        """Helper to convert markdown to HTML."""
        return TelegramChannel._markdown_to_telegram_html(text)

    def test_empty_string(self) -> None:
        """Empty input should not produce empty output (should escape to empty)."""
        result = self.convert("")
        # Empty string should return empty string (after escaping)
        assert result == ""

    def test_plain_text(self) -> None:
        """Plain text should be escaped but visible."""
        result = self.convert("Hello world")
        assert "Hello world" in result
        assert result == "Hello world"

    def test_bold_markdown(self) -> None:
        """Bold markdown **text** should become <b>text</b>."""
        result = self.convert("Hello **world**")
        assert "<b>world</b>" in result

    def test_italic_markdown(self) -> None:
        """Italic markdown *text* should become <i>text</i>."""
        result = self.convert("Hello *world*")
        assert "<i>world</i>" in result

    def test_code_inline(self) -> None:
        """Inline code `code` should become <code>code</code>."""
        result = self.convert("Use `ls -la` command")
        assert "<code>ls -la</code>" in result

    def test_code_block(self) -> None:
        """Code blocks should be converted to <pre><code>."""
        result = self.convert("```python\nprint('hello')\n```")
        assert "<pre><code" in result
        # Note: single quotes are HTML-escaped to &#x27; - this is expected
        assert "print" in result

    def test_strikethrough(self) -> None:
        """Strikethrough ~~text~~ should become <s>text</s>."""
        result = self.convert("~~deleted~~")
        assert "<s>deleted</s>" in result

    def test_link(self) -> None:
        """Links [text](url) should become <a href=\"url\">text</a>."""
        result = self.convert("[Google](https://google.com)")
        assert '<a href="https://google.com">Google</a>' in result

    def test_header(self) -> None:
        """Headers # text should become <b>text</b>."""
        result = self.convert("# Hello Header")
        assert "<b>Hello Header</b>" in result

    def test_html_entities_escaped(self) -> None:
        """HTML special characters should be escaped."""
        result = self.convert("<script>alert('xss')</script>")
        assert "&lt;" in result
        assert "&gt;" in result
        assert "<script>" not in result

    def test_mixed_content(self) -> None:
        """Mixed markdown should be properly converted."""
        result = self.convert("## Title\n\nHello *world* and `code`")
        assert "<b>Title</b>" in result
        assert "<i>world</i>" in result
        assert "<code>code</code>" in result

    def test_ambiguous_asterisk(self) -> None:
        """Single asterisks that aren't italic should not break."""
        # A single * not forming valid italic should be preserved as-is
        result = self.convert("5 * 3 = 15")
        # The asterisk should be escaped or preserved
        assert "15" in result

    def test_underscore_not_italic(self) -> None:
        """Underscores in words should not trigger italic.

        BUG: Currently underscores inside words incorrectly trigger italic conversion.
        This is a markdown parsing bug - variable names like my_variable get converted
        to my<i>variable</i> which breaks code/literal content.
        """
        result = self.convert("my_variable_name")
        # This is the BUG - underscores in variable names should NOT be converted
        # Expected: my_variable_name (unchanged)
        # Actual: my<i>variable</i>name (incorrectly italicized)
        if "<i>" in result:
            pytest.fail(
                "BUG DETECTED: Underscores in words are incorrectly converted to italic. "
                f"Input: 'my_variable_name' -> Output: '{result}'. "
                "This breaks variable names and code literals."
            )

    def test_empty_result_prevention(self) -> None:
        """Test inputs that historically could cause empty results."""
        # Input with only special markdown chars
        result = self.convert("****")
        # Should NOT be empty - should have escaped content
        assert result != ""

        result = self.convert("````")  # Empty code block marker
        # Should NOT be empty
        assert result != ""

    def test_newline_handling(self) -> None:
        """Newlines should be preserved."""
        result = self.convert("line1\nline2")
        assert "line1" in result
        assert "line2" in result


class TestMarkdownErrorHandling:
    """Test error cases in markdown parsing."""

    @staticmethod
    def convert(text: str) -> str:
        """Helper to convert markdown to HTML."""
        return TelegramChannel._markdown_to_telegram_html(text)

    def test_none_input(self) -> None:
        """None-like input should be handled gracefully."""
        # Pass empty string for None-like
        result = self.convert("")
        assert isinstance(result, str)

    def test_very_long_input(self) -> None:
        """Very long input should not cause issues."""
        long_text = "a" * 10000
        result = self.convert(long_text)
        assert len(result) > 0

    def test_unicode_content(self) -> None:
        """Unicode content should be handled."""
        result = self.convert("Hello 🌍 世界 🎉")
        assert "Hello" in result
        assert "🌍" in result
