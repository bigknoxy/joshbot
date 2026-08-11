package tools

import (
	"strings"
	"testing"
)

func TestParseExaCLISearchResults_PrettyPrintedJSON(t *testing.T) {
	output := `{
  "title": "Kansas City Royals vs. Texas Rangers - May 31, 2026",
  "url": "https://example.com/1",
  "text": "Royals beat Rangers 5-3 in extra innings"
}

{
  "title": "Royals 3-6 Rangers (May 31, 2026)",
  "url": "https://example.com/2",
  "text": "Game summary and box score"
}`

	results, err := parseExaCLISearchResults(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "Kansas City Royals vs. Texas Rangers - May 31, 2026" {
		t.Errorf("expected first title, got %q", results[0].Title)
	}
	if results[1].Title != "Royals 3-6 Rangers (May 31, 2026)" {
		t.Errorf("expected second title, got %q", results[1].Title)
	}
}

func TestParseExaCLISearchResults_SingleLineJSON(t *testing.T) {
	output := `{"title": "Result One", "url": "https://example.com/1", "text": "First result"}

{"title": "Result Two", "url": "https://example.com/2", "text": "Second result"}`

	results, err := parseExaCLISearchResults(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestParseExaCLISearchResults_EmptyOutput(t *testing.T) {
	results, err := parseExaCLISearchResults("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty output, got %d", len(results))
	}
}

func TestParseExaCLISearchResults_MalformedJSON(t *testing.T) {
	output := `not valid json`
	results, err := parseExaCLISearchResults(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for garbage input, got %d", len(results))
	}
}

func TestParseExaCLISearchResults_MixedValidAndInvalid(t *testing.T) {
	output := `{"title": "First", "url": "https://a.com", "text": "valid"}

not valid json

{"title": "Second", "url": "https://b.com", "text": "valid"}`

	results, err := parseExaCLISearchResults(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 valid results, got %d", len(results))
	}
}

func TestParseExaCLISearchResults_LongText(t *testing.T) {
	longText := strings.Repeat("a", 10000)
	output := `{
  "title": "Long Result",
  "url": "https://example.com/1",
  "text": "` + longText + `"
}`

	results, err := parseExaCLISearchResults(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}
