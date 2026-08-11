package memory

import (
	"testing"
	"time"
)

func TestFactID_Deterministic(t *testing.T) {
	t.Parallel()
	id1 := FactID(FactUserInfo, "User prefers dark mode")
	id2 := FactID(FactUserInfo, "User prefers dark mode")
	if id1 != id2 {
		t.Errorf("FactID should be deterministic: got %q and %q", id1, id2)
	}
}

func TestFactID_DifferentContent(t *testing.T) {
	t.Parallel()
	id1 := FactID(FactUserInfo, "User prefers dark mode")
	id2 := FactID(FactUserInfo, "User prefers light mode")
	if id1 == id2 {
		t.Error("FactID should differ for different content")
	}
}

func TestFactID_DifferentCategory(t *testing.T) {
	t.Parallel()
	// This test verifies that different categories produce different IDs
	idCat1 := FactID(FactUserInfo, "Same content")
	idCat2 := FactID(FactPreference, "Same content")
	if idCat1 == idCat2 {
		t.Error("FactID should differ for different categories")
	}
}

func TestFactID_WhitespaceNormalization(t *testing.T) {
	t.Parallel()
	id1 := FactID(FactUserInfo, "  User prefers dark mode  ")
	id2 := FactID(FactUserInfo, "User prefers dark mode")
	if id1 != id2 {
		t.Error("FactID should normalize whitespace")
	}
}

func TestFactID_CaseInsensitive(t *testing.T) {
	t.Parallel()
	id1 := FactID(FactUserInfo, "User Prefers Dark Mode")
	id2 := FactID(FactUserInfo, "user prefers dark mode")
	if id1 != id2 {
		t.Error("FactID should be case-insensitive")
	}
}

func TestFactID_ShortOutput(t *testing.T) {
	t.Parallel()
	id := FactID(FactUserInfo, "test")
	if len(id) != 16 {
		t.Errorf("FactID should be 16 hex chars (8 bytes), got %d: %q", len(id), id)
	}
}

func TestMergeFacts_HigherConfidenceWins(t *testing.T) {
	t.Parallel()
	a := Fact{Content: "old", Confidence: 0.5, UpdatedAt: time.Unix(1000, 0)}
	b := Fact{Content: "new", Confidence: 0.9, UpdatedAt: time.Unix(2000, 0)}

	merged := MergeFacts(a, b)
	if merged.Content != "new" {
		t.Errorf("expected higher confidence content 'new', got %q", merged.Content)
	}
	if merged.Confidence != 0.9 {
		t.Errorf("expected confidence 0.9, got %v", merged.Confidence)
	}
}

func TestMergeFacts_EqualConfidenceLaterWins(t *testing.T) {
	t.Parallel()
	a := Fact{Content: "old", Confidence: 0.8, UpdatedAt: time.Unix(1000, 0)}
	b := Fact{Content: "new", Confidence: 0.8, UpdatedAt: time.Unix(2000, 0)}

	merged := MergeFacts(a, b)
	if merged.Content != "new" {
		t.Errorf("expected later content 'new', got %q", merged.Content)
	}
}

func TestMergeFacts_LowerConfidenceKeepsOld(t *testing.T) {
	t.Parallel()
	a := Fact{Content: "old", Confidence: 0.9, UpdatedAt: time.Unix(1000, 0)}
	b := Fact{Content: "new", Confidence: 0.3, UpdatedAt: time.Unix(2000, 0)}

	merged := MergeFacts(a, b)
	if merged.Content != "old" {
		t.Errorf("expected old content to be kept, got %q", merged.Content)
	}
}

func TestMergeFacts_AccessCountMax(t *testing.T) {
	t.Parallel()
	a := Fact{AccessCount: 5}
	b := Fact{AccessCount: 10}

	merged := MergeFacts(a, b)
	if merged.AccessCount != 10 {
		t.Errorf("expected max access count 10, got %d", merged.AccessCount)
	}
}

func TestMergeFacts_TagsMergedDeduplicated(t *testing.T) {
	t.Parallel()
	a := Fact{Tags: []string{"work", "frontend"}}
	b := Fact{Tags: []string{"frontend", "backend"}}

	merged := MergeFacts(a, b)
	expected := []string{"work", "frontend", "backend"}
	if len(merged.Tags) != len(expected) {
		t.Fatalf("expected %d tags, got %d: %v", len(expected), len(merged.Tags), merged.Tags)
	}
	for i, tag := range expected {
		if merged.Tags[i] != tag {
			t.Errorf("tag[%d] = %q, want %q", i, merged.Tags[i], tag)
		}
	}
}

func TestMergeFacts_EmptyTags(t *testing.T) {
	t.Parallel()
	a := Fact{Content: "test", Confidence: 0.5}
	b := Fact{Content: "test", Confidence: 0.5}

	merged := MergeFacts(a, b)
	if len(merged.Tags) != 0 {
		t.Errorf("expected no tags, got %v", merged.Tags)
	}
}

func TestClampConfidence_BelowZero(t *testing.T) {
	if ClampConfidence(-0.5) != 0 {
		t.Error("ClampConfidence(-0.5) should return 0")
	}
}

func TestClampConfidence_AboveOne(t *testing.T) {
	if ClampConfidence(1.5) != 1 {
		t.Error("ClampConfidence(1.5) should return 1")
	}
}

func TestClampConfidence_ValidRange(t *testing.T) {
	if ClampConfidence(0.5) != 0.5 {
		t.Error("ClampConfidence(0.5) should return 0.5")
	}
}

func TestClampConfidence_Boundaries(t *testing.T) {
	if ClampConfidence(0) != 0 {
		t.Error("ClampConfidence(0) should return 0")
	}
	if ClampConfidence(1) != 1 {
		t.Error("ClampConfidence(1) should return 1")
	}
}
