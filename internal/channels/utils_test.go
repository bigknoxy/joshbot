package channels

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bigknoxy/joshbot/internal/bus"
	"github.com/bigknoxy/joshbot/internal/config"
)

func TestSplitMessage_ShortContent(t *testing.T) {
	content := "Hello, world!"
	parts := splitMessage(content, 4096)
	if len(parts) != 1 {
		t.Errorf("expected 1 part, got %d", len(parts))
	}
	if parts[0] != content {
		t.Errorf("expected %q, got %q", content, parts[0])
	}
}

func TestSplitMessage_EmptyContent(t *testing.T) {
	parts := splitMessage("", 4096)
	if len(parts) != 1 {
		t.Errorf("expected 1 part for empty content, got %d", len(parts))
	}
	if parts[0] != "" {
		t.Errorf("expected empty string, got %q", parts[0])
	}
}

func TestSplitMessage_ExactMaxLength(t *testing.T) {
	content := strings.Repeat("a", 4096)
	parts := splitMessage(content, 4096)
	if len(parts) != 1 {
		t.Errorf("expected 1 part for exact max length, got %d", len(parts))
	}
}

func TestSplitMessage_LongContent(t *testing.T) {
	content := strings.Repeat("a", 10000)
	parts := splitMessage(content, 4096)
	if len(parts) < 2 {
		t.Errorf("expected at least 2 parts for 10000 chars, got %d", len(parts))
	}
	// Verify total content is preserved
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	// Total should be close to original (may differ due to code fence handling)
	if total < 10000 {
		t.Errorf("total length %d is less than original 10000", total)
	}
}

func TestSplitMessage_SplitsOnNewline(t *testing.T) {
	// Create content with newlines at split points
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "line " + string(rune('a'+i%26)) + string(rune('a'+i%26))
	}
	content := strings.Join(lines, "\n")

	parts := splitMessage(content, 100)
	if len(parts) < 2 {
		t.Errorf("expected at least 2 parts, got %d", len(parts))
	}
}

func TestSplitMessage_CodeBlockHandling(t *testing.T) {
	// Content with code blocks that span multiple parts
	codeContent := "```go\n" + strings.Repeat("fmt.Println(\"hello\")\n", 200) + "```"
	parts := splitMessage(codeContent, 500)

	if len(parts) < 2 {
		t.Errorf("expected at least 2 parts for code block, got %d", len(parts))
	}

	// Verify code fences are balanced across parts
	totalFences := 0
	for _, p := range parts {
		totalFences += strings.Count(p, "```")
	}
	if totalFences%2 != 0 {
		t.Errorf("total code fences %d is odd (unbalanced)", totalFences)
	}
}

func TestSplitMessage_NoNewlineHardSplit(t *testing.T) {
	// Content without newlines that needs hard splitting
	content := strings.Repeat("x", 5000)
	parts := splitMessage(content, 1000)

	if len(parts) < 5 {
		t.Errorf("expected at least 5 parts, got %d", len(parts))
	}

	// Each part should be <= maxLen + 4 (for code fence handling)
	for i, p := range parts {
		if len(p) > 1004 {
			t.Errorf("part %d length %d exceeds max 1004", i, len(p))
		}
	}
}

func TestIsRetryable_NetworkError(t *testing.T) {
	err := &testError{"network timeout"}
	if !isRetryable(err) {
		t.Error("expected network error to be retryable")
	}
}

func TestIsRetryable_TimeoutError(t *testing.T) {
	err := &testError{"connection timeout"}
	if !isRetryable(err) {
		t.Error("expected timeout error to be retryable")
	}
}

func TestIsRetryable_ConnectionError(t *testing.T) {
	err := &testError{"connection refused"}
	if !isRetryable(err) {
		t.Error("expected connection error to be retryable")
	}
}

func TestIsRetryable_TooManyRequests(t *testing.T) {
	err := &testError{"too many requests"}
	if !isRetryable(err) {
		t.Error("expected 'too many requests' to be retryable")
	}
}

func TestIsRetryable_RetryAfter(t *testing.T) {
	err := &testError{"retry after 10 seconds"}
	if !isRetryable(err) {
		t.Error("expected 'retry after' to be retryable")
	}
}

func TestIsRetryable_BotBlocked(t *testing.T) {
	err := &testError{"bot was blocked by the user"}
	if !isRetryable(err) {
		t.Error("expected 'bot was blocked' to be retryable")
	}
}

func TestIsRetryable_UserDeactivated(t *testing.T) {
	err := &testError{"user is deactivated"}
	if !isRetryable(err) {
		t.Error("expected 'user is deactivated' to be retryable")
	}
}

func TestIsRetryable_MessageToReply(t *testing.T) {
	err := &testError{"message to reply not found"}
	if isRetryable(err) {
		t.Error("expected 'message to reply' to NOT be retryable")
	}
}

func TestIsRetryable_ChatNotFound(t *testing.T) {
	err := &testError{"chat not found"}
	if isRetryable(err) {
		t.Error("expected 'chat not found' to NOT be retryable")
	}
}

func TestIsRetryable_UnknownError(t *testing.T) {
	err := &testError{"some unknown error"}
	if !isRetryable(err) {
		t.Error("expected unknown error to be retryable (default)")
	}
}

func TestIsRetryable_NilError(t *testing.T) {
	// isRetryable with nil error would panic, so we don't test that
	// But we can test with an empty error message
	err := &testError{""}
	if !isRetryable(err) {
		t.Error("expected empty error to be retryable (default)")
	}
}

func TestParseMarkdown_Bold(t *testing.T) {
	tg := &TelegramChannel{}
	result, err := tg.ParseMarkdown("**bold text**")
	if err != nil {
		t.Fatalf("ParseMarkdown() error = %v", err)
	}
	if !strings.Contains(result, "<b>bold text</b>") {
		t.Errorf("expected <b>bold text</b> in result, got %q", result)
	}
}

func TestParseMarkdown_Italic(t *testing.T) {
	tg := &TelegramChannel{}
	result, err := tg.ParseMarkdown("__italic text__")
	if err != nil {
		t.Fatalf("ParseMarkdown() error = %v", err)
	}
	if !strings.Contains(result, "<i>italic text</i>") {
		t.Errorf("expected <i>italic text</i> in result, got %q", result)
	}
}

func TestParseMarkdown_Code(t *testing.T) {
	tg := &TelegramChannel{}
	result, err := tg.ParseMarkdown("`code`")
	if err != nil {
		t.Fatalf("ParseMarkdown() error = %v", err)
	}
	if !strings.Contains(result, "<code>code</code>") {
		t.Errorf("expected <code>code</code> in result, got %q", result)
	}
}

func TestParseMarkdown_Pre(t *testing.T) {
	tg := &TelegramChannel{}
	result, err := tg.ParseMarkdown("```preformatted```")
	if err != nil {
		t.Fatalf("ParseMarkdown() error = %v", err)
	}
	if !strings.Contains(result, "<pre>preformatted</pre>") {
		t.Errorf("expected <pre>preformatted</pre> in result, got %q", result)
	}
}

func TestParseMarkdown_Link(t *testing.T) {
	tg := &TelegramChannel{}
	result, err := tg.ParseMarkdown("[text](https://example.com)")
	if err != nil {
		t.Fatalf("ParseMarkdown() error = %v", err)
	}
	if !strings.Contains(result, `<a href="https://example.com">text</a>`) {
		t.Errorf("expected link in result, got %q", result)
	}
}

func TestParseMarkdown_Combined(t *testing.T) {
	tg := &TelegramChannel{}
	result, err := tg.ParseMarkdown("**bold** and `code` and [link](http://x.com)")
	if err != nil {
		t.Fatalf("ParseMarkdown() error = %v", err)
	}
	if !strings.Contains(result, "<b>bold</b>") {
		t.Error("expected bold in result")
	}
	if !strings.Contains(result, "<code>code</code>") {
		t.Error("expected code in result")
	}
	if !strings.Contains(result, `<a href="http://x.com">link</a>`) {
		t.Error("expected link in result")
	}
}

func TestParseMarkdown_EmptyString(t *testing.T) {
	tg := &TelegramChannel{}
	result, err := tg.ParseMarkdown("")
	if err != nil {
		t.Fatalf("ParseMarkdown() error = %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestStripHTML_EmptyString(t *testing.T) {
	result := stripHTML("")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestStripHTML_NoTags(t *testing.T) {
	result := stripHTML("plain text")
	if result != "plain text" {
		t.Errorf("expected 'plain text', got %q", result)
	}
}

func TestStripHTML_WithTags(t *testing.T) {
	result := stripHTML("<b>bold</b> text")
	if result != "bold text" {
		t.Errorf("expected 'bold text', got %q", result)
	}
}

func TestStripHTML_MultipleTags(t *testing.T) {
	result := stripHTML("<div><p>Hello</p><p>World</p></div>")
	if result != "HelloWorld" {
		t.Errorf("expected 'HelloWorld', got %q", result)
	}
}

func TestStripHTML_SelfClosingTag(t *testing.T) {
	result := stripHTML("text<br/>more text")
	if result != "textmore text" {
		t.Errorf("expected 'textmore text', got %q", result)
	}
}

func TestStripHTML_NestedTags(t *testing.T) {
	result := stripHTML("<div><span>nested</span></div>")
	if result != "nested" {
		t.Errorf("expected 'nested', got %q", result)
	}
}

func TestCLIChannel_Send(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)

	err := cli.Send(bus.OutboundMessage{
		Content: "test message",
	})
	if err != nil {
		t.Errorf("Send() error = %v", err)
	}
}

func TestCLIChannel_Send_WithMetadata(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)

	err := cli.Send(bus.OutboundMessage{
		Content: "test message",
		Metadata: map[string]any{
			"reply_to": "user123",
		},
	})
	if err != nil {
		t.Errorf("Send() error = %v", err)
	}
}

func TestCLIChannel_ConsumeOutbound(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)

	// Send a message through the outbound channel
	msgBus.OutboundChan() <- bus.OutboundMessage{
		Content: "test outbound message",
	}

	// Give the consumer time to process
	time.Sleep(50 * time.Millisecond)

	// The message should have been consumed without error
	_ = cli // reference to avoid unused variable
}

func TestCLIChannel_PrintHelp(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.printHelp()
	// Just verify it doesn't panic
}

func TestCLIChannel_PrintWelcome(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.printWelcome()
	// Just verify it doesn't panic
}

func TestCLIChannel_PrintHistory_Empty(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.printHistory()
	// Just verify it doesn't panic with empty history
}

func TestCLIChannel_PrintHistory_WithEntries(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	ctx := context.Background()
	cli.processInput(ctx, "command1")
	cli.processInput(ctx, "command2")
	cli.printHistory()
	// Just verify it doesn't panic with entries
}

func TestCLIChannel_PrintError(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.printError("test error")
	// Just verify it doesn't panic
}

func TestCLIChannel_PrintInfo(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.printInfo("test info")
	// Just verify it doesn't panic
}

func TestCLIChannel_PrintSuccess(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.printSuccess("test success")
	// Just verify it doesn't panic
}

func TestCLIChannel_FormatAndPrintMessage(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.formatAndPrintMessage(bus.OutboundMessage{
		Content: "formatted message",
	})
	// Just verify it doesn't panic
}

func TestCLIChannel_FormatAndPrintMessage_WithReplyTo(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.formatAndPrintMessage(bus.OutboundMessage{
		Content: "reply message",
		Metadata: map[string]any{
			"reply_to": "user123",
		},
	})
	// Just verify it doesn't panic
}

func TestCLIChannel_FormatAndPrintMessage_HTMLMode(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.formatAndPrintMessage(bus.OutboundMessage{
		Content: "<b>HTML content</b>",
		Metadata: map[string]any{
			"parse_mode": "html",
		},
	})
	// Just verify it doesn't panic
}

func TestCLIChannel_FormatAndPrintMessage_MarkdownMode(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	cli.formatAndPrintMessage(bus.OutboundMessage{
		Content: "**bold content**",
		Metadata: map[string]any{
			"parse_mode": "markdown",
		},
	})
	// Just verify it doesn't panic
}

func TestCLIChannel_AddToHistory(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	ctx := context.Background()

	cli.processInput(ctx, "first command")
	cli.processInput(ctx, "second command")
	cli.processInput(ctx, "third command")

	if len(cli.inputHistory) != 3 {
		t.Errorf("expected 3 history items, got %d", len(cli.inputHistory))
	}
}

func TestCLIChannel_AddToHistory_ConsecutiveDuplicates(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cli := NewCLIChannel(msgBus)
	ctx := context.Background()

	cli.processInput(ctx, "same command")
	cli.processInput(ctx, "same command") // consecutive duplicate, should not be added

	if len(cli.inputHistory) != 1 {
		t.Errorf("expected 1 history item (consecutive duplicate not added), got %d", len(cli.inputHistory))
	}
}

func TestTelegramChannel_MessageSig(t *testing.T) {
	em := &editableMessage{
		messageID: 42,
		chatID:    100,
	}

	sig, chatID := em.MessageSig()
	if sig != "42" {
		t.Errorf("sig = %q, want '42'", sig)
	}
	if chatID != 100 {
		t.Errorf("chatID = %d, want 100", chatID)
	}
}

func TestTelegramChannel_BuildReplyMarkup_Empty(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := &config.TelegramConfig{
		Token: "test_token",
	}
	tg := NewTelegramChannel(msgBus, cfg)

	rm := tg.buildReplyMarkup(nil)
	if rm == nil {
		t.Error("expected non-nil ReplyMarkup")
	}
}

func TestTelegramChannel_BuildReplyMarkup_WithInlineKeyboard(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := &config.TelegramConfig{
		Token: "test_token",
	}
	tg := NewTelegramChannel(msgBus, cfg)

	markup := map[string]any{
		"inline_keyboard": [][]map[string]any{
			{
				{"text": "Button 1", "callback_data": "btn1"},
			},
		},
	}

	rm := tg.buildReplyMarkup(markup)
	if rm == nil {
		t.Fatal("expected non-nil ReplyMarkup")
	}
}

// testError is a simple error type for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
