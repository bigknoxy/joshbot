package main

import (
	"strings"
	"testing"
)

// --- indexOf ---

func TestIndexOf_Found(t *testing.T) {
	haystack := []string{"a", "b", "c", "d"}
	if got := indexOf(haystack, "c"); got != 2 {
		t.Errorf("indexOf() = %d, want 2", got)
	}
}

func TestIndexOf_NotFound(t *testing.T) {
	haystack := []string{"a", "b", "c"}
	if got := indexOf(haystack, "z"); got != -1 {
		t.Errorf("indexOf() = %d, want -1", got)
	}
}

func TestIndexOf_EmptySlice(t *testing.T) {
	if got := indexOf([]string{}, "x"); got != -1 {
		t.Errorf("indexOf() = %d, want -1", got)
	}
}

func TestIndexOf_FirstElement(t *testing.T) {
	haystack := []string{"first", "second", "third"}
	if got := indexOf(haystack, "first"); got != 0 {
		t.Errorf("indexOf() = %d, want 0", got)
	}
}

func TestIndexOf_LastElement(t *testing.T) {
	haystack := []string{"first", "second", "third"}
	if got := indexOf(haystack, "third"); got != 2 {
		t.Errorf("indexOf() = %d, want 2", got)
	}
}

func TestIndexOf_NilSlice(t *testing.T) {
	if got := indexOf(nil, "x"); got != -1 {
		t.Errorf("indexOf() = %d, want -1", got)
	}
}

// --- getVersion ---

func TestGetVersion_Dev(t *testing.T) {
	old := Version
	Version = "dev"
	defer func() { Version = old }()
	if got := getVersion(); got != "dev" {
		t.Errorf("getVersion() = %q, want 'dev'", got)
	}
}

func TestGetVersion_WithVPrefix(t *testing.T) {
	old := Version
	Version = "v1.2.3"
	defer func() { Version = old }()
	if got := getVersion(); got != "v1.2.3" {
		t.Errorf("getVersion() = %q, want 'v1.2.3'", got)
	}
}

func TestGetVersion_WithoutVPrefix(t *testing.T) {
	old := Version
	Version = "1.2.3"
	defer func() { Version = old }()
	if got := getVersion(); got != "v1.2.3" {
		t.Errorf("getVersion() = %q, want 'v1.2.3'", got)
	}
}

func TestGetVersion_EmptyString(t *testing.T) {
	old := Version
	Version = ""
	defer func() { Version = old }()
	if got := getVersion(); got != "v" {
		t.Errorf("getVersion() = %q, want 'v'", got)
	}
}

// --- compareVersions ---

func TestCompareVersions_Equal(t *testing.T) {
	if got := compareVersions("1.2.3", "1.2.3"); got != 0 {
		t.Errorf("compareVersions() = %d, want 0", got)
	}
}

func TestCompareVersions_LessThan(t *testing.T) {
	if got := compareVersions("1.2.2", "1.2.3"); got != -1 {
		t.Errorf("compareVersions() = %d, want -1", got)
	}
}

func TestCompareVersions_GreaterThan(t *testing.T) {
	if got := compareVersions("1.2.4", "1.2.3"); got != 1 {
		t.Errorf("compareVersions() = %d, want 1", got)
	}
}

func TestCompareVersions_WithVPrefix(t *testing.T) {
	if got := compareVersions("v1.2.3", "1.2.3"); got != 0 {
		t.Errorf("compareVersions() = %d, want 0", got)
	}
}

func TestCompareVersions_MajorDifference(t *testing.T) {
	if got := compareVersions("2.0.0", "1.9.9"); got != 1 {
		t.Errorf("compareVersions() = %d, want 1", got)
	}
}

func TestCompareVersions_MinorDifference(t *testing.T) {
	if got := compareVersions("1.3.0", "1.2.9"); got != 1 {
		t.Errorf("compareVersions() = %d, want 1", got)
	}
}

func TestCompareVersions_WithPrerelease(t *testing.T) {
	// Prerelease suffixes should be ignored
	if got := compareVersions("1.2.3-beta", "1.2.3"); got != 0 {
		t.Errorf("compareVersions() = %d, want 0 (prerelease ignored)", got)
	}
}

func TestCompareVersions_BothPrerelease(t *testing.T) {
	if got := compareVersions("1.2.3-rc1", "1.2.3-rc2"); got != 0 {
		t.Errorf("compareVersions() = %d, want 0 (prerelease ignored)", got)
	}
}

func TestCompareVersions_ShortVersion(t *testing.T) {
	// Versions with fewer parts should still work
	if got := compareVersions("1.2", "1.2.0"); got != 0 {
		t.Errorf("compareVersions() = %d, want 0", got)
	}
}

// --- stripPrerelease ---

func TestStripPrerelease_NoPrerelease(t *testing.T) {
	if got := stripPrerelease("1.2.3"); got != "1.2.3" {
		t.Errorf("stripPrerelease() = %q, want '1.2.3'", got)
	}
}

func TestStripPrerelease_WithPrerelease(t *testing.T) {
	if got := stripPrerelease("1.2.3-beta"); got != "1.2.3" {
		t.Errorf("stripPrerelease() = %q, want '1.2.3'", got)
	}
}

func TestStripPrerelease_WithRC(t *testing.T) {
	if got := stripPrerelease("1.2.3-rc.1"); got != "1.2.3" {
		t.Errorf("stripPrerelease() = %q, want '1.2.3'", got)
	}
}

func TestStripPrerelease_EmptyString(t *testing.T) {
	if got := stripPrerelease(""); got != "" {
		t.Errorf("stripPrerelease() = %q, want ''", got)
	}
}

func TestStripPrerelease_OnlyPrerelease(t *testing.T) {
	if got := stripPrerelease("-beta"); got != "" {
		t.Errorf("stripPrerelease() = %q, want ''", got)
	}
}

// --- formatSize ---

func TestFormatSize_Zero(t *testing.T) {
	if got := formatSize(0); got != "0 B" {
		t.Errorf("formatSize() = %q, want '0 B'", got)
	}
}

func TestFormatSize_Bytes(t *testing.T) {
	if got := formatSize(512); got != "512 B" {
		t.Errorf("formatSize() = %q, want '512 B'", got)
	}
}

func TestFormatSize_Kilobytes(t *testing.T) {
	if got := formatSize(1024); got != "1.0 KB" {
		t.Errorf("formatSize() = %q, want '1.0 KB'", got)
	}
}

func TestFormatSize_Megabytes(t *testing.T) {
	if got := formatSize(1024 * 1024); got != "1.0 MB" {
		t.Errorf("formatSize() = %q, want '1.0 MB'", got)
	}
}

func TestFormatSize_Gigabytes(t *testing.T) {
	if got := formatSize(1024 * 1024 * 1024); got != "1.0 GB" {
		t.Errorf("formatSize() = %q, want '1.0 GB'", got)
	}
}

func TestFormatSize_Terabytes(t *testing.T) {
	if got := formatSize(1024 * 1024 * 1024 * 1024); got != "1.0 TB" {
		t.Errorf("formatSize() = %q, want '1.0 TB'", got)
	}
}

func TestFormatSize_PartialKilobyte(t *testing.T) {
	if got := formatSize(1536); got != "1.5 KB" {
		t.Errorf("formatSize() = %q, want '1.5 KB'", got)
	}
}

func TestFormatSize_Negative(t *testing.T) {
	// Negative values are less than unit, so they show as bytes
	if got := formatSize(-100); got != "-100 B" {
		t.Errorf("formatSize() = %q, want '-100 B'", got)
	}
}

// --- filterModels ---

func TestFilterModels_NoFilter(t *testing.T) {
	models := []string{"gpt-4", "gpt-3.5", "claude-3"}
	result := filterModels(models, "")
	if len(result) != 3 {
		t.Errorf("filterModels() returned %d models, want 3", len(result))
	}
}

func TestFilterModels_MatchingFilter(t *testing.T) {
	models := []string{"gpt-4", "gpt-3.5", "claude-3"}
	result := filterModels(models, "gpt")
	if len(result) != 2 {
		t.Errorf("filterModels() returned %d models, want 2", len(result))
	}
	for _, m := range result {
		if !strings.Contains(m, "gpt") {
			t.Errorf("filterModels() returned %q which doesn't match 'gpt'", m)
		}
	}
}

func TestFilterModels_NoMatch(t *testing.T) {
	models := []string{"gpt-4", "gpt-3.5", "claude-3"}
	result := filterModels(models, "nonexistent")
	if len(result) != 0 {
		t.Errorf("filterModels() returned %d models, want 0", len(result))
	}
}

func TestFilterModels_CaseInsensitive(t *testing.T) {
	models := []string{"GPT-4", "gpt-3.5", "Claude-3"}
	result := filterModels(models, "gpt")
	if len(result) != 2 {
		t.Errorf("filterModels() returned %d models, want 2 (case insensitive)", len(result))
	}
}

func TestFilterModels_EmptyModels(t *testing.T) {
	result := filterModels([]string{}, "gpt")
	if len(result) != 0 {
		t.Errorf("filterModels() returned %d models, want 0", len(result))
	}
}

func TestFilterModels_PartialMatch(t *testing.T) {
	models := []string{"llama-3-8b", "llama-3-70b", "gpt-4"}
	result := filterModels(models, "llama")
	if len(result) != 2 {
		t.Errorf("filterModels() returned %d models, want 2", len(result))
	}
}

// --- boolToEnabled ---

func TestBoolToEnabled_True(t *testing.T) {
	if got := boolToEnabled(true); got != "enabled" {
		t.Errorf("boolToEnabled(true) = %q, want 'enabled'", got)
	}
}

func TestBoolToEnabled_False(t *testing.T) {
	if got := boolToEnabled(false); got != "disabled" {
		t.Errorf("boolToEnabled(false) = %q, want 'disabled'", got)
	}
}

// --- statusBool ---

func TestStatusBool_True(t *testing.T) {
	if got := statusBool(true); got != "(exists)" {
		t.Errorf("statusBool(true) = %q, want '(exists)'", got)
	}
}

func TestStatusBool_False(t *testing.T) {
	if got := statusBool(false); got != "(missing)" {
		t.Errorf("statusBool(false) = %q, want '(missing)'", got)
	}
}

// --- maskToken ---

func TestMaskToken_Empty(t *testing.T) {
	if got := maskToken(""); got != "" {
		t.Errorf("maskToken('') = %q, want ''", got)
	}
}

func TestMaskToken_ShortToken(t *testing.T) {
	// Token <= 16 chars: shows first 4 and last 4
	got := maskToken("1234567890123456")
	if !strings.Contains(got, "****") {
		t.Errorf("maskToken() = %q, expected to contain '****'", got)
	}
}

func TestMaskToken_TelegramToken(t *testing.T) {
	// Standard Telegram token format: "id:secret"
	token := "1234567890:ABCdefGHIjklMNOpqrsTUVwxyz"
	got := maskToken(token)
	// Should contain the id and masked secret
	if !strings.HasPrefix(got, "1234567890:") {
		t.Errorf("maskToken() = %q, expected to start with '1234567890:'", got)
	}
	if !strings.Contains(got, "****") {
		t.Errorf("maskToken() = %q, expected to contain '****'", got)
	}
	// Last 4 chars of the secret should be visible
	if !strings.HasSuffix(got, "wxyz") {
		t.Errorf("maskToken() = %q, expected to end with 'wxyz'", got)
	}
}

func TestMaskToken_NoColonLong(t *testing.T) {
	// Long token without colon
	token := "abcdefghijklmnopqrstuvwxyz1234567890"
	got := maskToken(token)
	if !strings.Contains(got, "****") {
		t.Errorf("maskToken() = %q, expected to contain '****'", got)
	}
}

func TestMaskToken_Exactly16Chars(t *testing.T) {
	token := "1234567890123456"
	got := maskToken(token)
	if !strings.Contains(got, "****") {
		t.Errorf("maskToken() = %q, expected to contain '****'", got)
	}
}

// --- sanitizeToken ---

func TestSanitizeToken_CleanToken(t *testing.T) {
	token := "1234567890:ABCdefGHIjklMNOpqrsTUVwxyz"
	if got := sanitizeToken(token); got != token {
		t.Errorf("sanitizeToken() = %q, want %q", got, token)
	}
}

func TestSanitizeToken_RemovesControlChars(t *testing.T) {
	// Token with ANSI escape sequence
	token := "1234567890:ABC\x1b[CdefGHIjklMNOpqrsTUVwxyz"
	got := sanitizeToken(token)
	if strings.Contains(got, "\x1b") {
		t.Errorf("sanitizeToken() = %q, expected to remove escape char", got)
	}
}

func TestSanitizeToken_RemovesNullBytes(t *testing.T) {
	token := "1234\x00567890:ABCdef"
	got := sanitizeToken(token)
	if strings.Contains(got, "\x00") {
		t.Errorf("sanitizeToken() = %q, expected to remove null bytes", got)
	}
}

func TestSanitizeToken_EmptyString(t *testing.T) {
	if got := sanitizeToken(""); got != "" {
		t.Errorf("sanitizeToken('') = %q, want ''", got)
	}
}

func TestSanitizeToken_OnlyControlChars(t *testing.T) {
	token := "\x00\x01\x02\x03"
	got := sanitizeToken(token)
	if got != "" {
		t.Errorf("sanitizeToken() = %q, want '' (all control chars removed)", got)
	}
}

func TestSanitizeToken_PreservesPrintableASCII(t *testing.T) {
	token := "abc123!@#$%^&*()"
	got := sanitizeToken(token)
	if got != token {
		t.Errorf("sanitizeToken() = %q, want %q", got, token)
	}
}

// --- getPersonalitySoul ---

func TestGetPersonalitySoul_Professional(t *testing.T) {
	got := getPersonalitySoul("1")
	if !strings.Contains(got, "# Soul") {
		t.Error("expected '# Soul' in professional personality")
	}
	if !strings.Contains(got, "professional, efficient, and focused") {
		t.Error("expected professional personality content")
	}
}

func TestGetPersonalitySoul_Friendly(t *testing.T) {
	got := getPersonalitySoul("2")
	if !strings.Contains(got, "# Soul") {
		t.Error("expected '# Soul' in friendly personality")
	}
	if !strings.Contains(got, "warm, approachable") {
		t.Error("expected friendly personality content")
	}
}

func TestGetPersonalitySoul_Sarcastic(t *testing.T) {
	got := getPersonalitySoul("3")
	if !strings.Contains(got, "# Soul") {
		t.Error("expected '# Soul' in sarcastic personality")
	}
	if !strings.Contains(got, "sharp wit") {
		t.Error("expected sarcastic personality content")
	}
}

func TestGetPersonalitySoul_Minimal(t *testing.T) {
	got := getPersonalitySoul("4")
	if !strings.Contains(got, "# Soul") {
		t.Error("expected '# Soul' in minimal personality")
	}
	if !strings.Contains(got, "Maximum information, minimum words") {
		t.Error("expected minimal personality content")
	}
}

func TestGetPersonalitySoul_Custom(t *testing.T) {
	got := getPersonalitySoul("5")
	if !strings.Contains(got, "# Soul") {
		t.Error("expected '# Soul' in custom personality")
	}
	if !strings.Contains(got, "Write your personality here") {
		t.Error("expected custom personality content")
	}
}

func TestGetPersonalitySoul_Default(t *testing.T) {
	got := getPersonalitySoul("unknown")
	if !strings.Contains(got, "# Soul") {
		t.Error("expected '# Soul' in default personality")
	}
	if !strings.Contains(got, "Write your personality here") {
		t.Error("expected default personality content")
	}
}

func TestGetPersonalitySoul_Empty(t *testing.T) {
	got := getPersonalitySoul("")
	if !strings.Contains(got, "# Soul") {
		t.Error("expected '# Soul' in empty personality")
	}
}

func TestGetPersonalitySoul_AllHaveSoulHeader(t *testing.T) {
	choices := []string{"1", "2", "3", "4", "5", "custom", ""}
	for _, choice := range choices {
		got := getPersonalitySoul(choice)
		if !strings.Contains(got, "# Soul") {
			t.Errorf("getPersonalitySoul(%q) missing '# Soul' header", choice)
		}
		if !strings.Contains(got, "Personality") {
			t.Errorf("getPersonalitySoul(%q) missing 'Personality' section", choice)
		}
		if !strings.Contains(got, "Communication Style") {
			t.Errorf("getPersonalitySoul(%q) missing 'Communication Style' section", choice)
		}
	}
}
