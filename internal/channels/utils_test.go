package channels

import (
	"strings"
	"testing"

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

// Telegram's parse-entity failures must never be treated as ordinary
// retryable errors here: retrying the same formatted content would just fail
// again. isParseEntityError, not isRetryable, is what drives the plain-text
// fallback in Send.
func TestIsParseEntityError_CantParseEntities(t *testing.T) {
	err := &testError{"telegram: Bad Request: can't parse entities: Character '_' is reserved and must be escaped (400)"}
	if !isParseEntityError(err) {
		t.Error("expected 'can't parse entities' to be detected as a parse-entity error")
	}
}

func TestIsParseEntityError_CantParseMessageText(t *testing.T) {
	err := &testError{"telegram: Bad Request: can't parse message text: unclosed markup (400)"}
	if !isParseEntityError(err) {
		t.Error("expected 'can't parse message text' to be detected as a parse-entity error")
	}
}

func TestIsParseEntityError_UnsupportedStartTag(t *testing.T) {
	err := &testError{"telegram: Bad Request: unsupported start tag \"foo\" at byte offset 3 (400)"}
	if !isParseEntityError(err) {
		t.Error("expected 'unsupported start tag' to be detected as a parse-entity error")
	}
}

func TestIsParseEntityError_Unclosed(t *testing.T) {
	err := &testError{"telegram: Bad Request: can't find end of the entity starting at byte offset 0, unclosed (400)"}
	if !isParseEntityError(err) {
		t.Error("expected 'unclosed' to be detected as a parse-entity error")
	}
}

func TestIsParseEntityError_CaseInsensitive(t *testing.T) {
	err := &testError{"telegram: Bad Request: CAN'T PARSE ENTITIES: whoops (400)"}
	if !isParseEntityError(err) {
		t.Error("expected match to be case-insensitive")
	}
}

// Other 400s (e.g. chat not found) must not be silently downgraded to plain
// text — matching on "400" alone would wrongly catch these.
func TestIsParseEntityError_OtherBadRequestNotMatched(t *testing.T) {
	err := &testError{"telegram: Bad Request: chat not found (400)"}
	if isParseEntityError(err) {
		t.Error("a generic 400 must not be treated as a parse-entity error")
	}
}

func TestIsParseEntityError_NonBadRequestNotMatched(t *testing.T) {
	err := &testError{"telegram: Forbidden: bot was blocked by the user (403)"}
	if isParseEntityError(err) {
		t.Error("a non-parse error must not be treated as a parse-entity error")
	}
}

func TestIsParseEntityError_NilError(t *testing.T) {
	if isParseEntityError(nil) {
		t.Error("nil error must not be treated as a parse-entity error")
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
