package providers

import (
	"testing"
)

func TestNormalizeProviderName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"canonical_openai", "openai", "openai"},
		{"canonical_anthropic", "anthropic", "anthropic"},
		{"canonical_gemini", "gemini", "gemini"},
		{"alias_google_to_gemini", "google", "gemini"},
		{"alias_local_to_ollama", "local", "ollama"},
		{"alias_nim_to_nvidia", "nim", "nvidia"},
		{"canonical_ollama", "ollama", "ollama"},
		{"canonical_nvidia", "nvidia", "nvidia"},
		{"case_insensitive", "OpenAI", "openai"},
		{"mixed_case_google", "Google", "gemini"},
		{"unknown_provider", "custom-provider", "custom-provider"},
		{"empty_string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeProviderName(tt.input)
			if got != tt.want {
				t.Errorf("normalizeProviderName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRegisterAndUnregisterProvider(t *testing.T) {
	registryLock.Lock()
	delete(registry, "test-e2e-provider")
	registryLock.Unlock()

	RegisterProvider("test-e2e-provider", func(cfg Config) (Provider, error) {
		return nil, nil
	})

	if !IsProviderRegistered("test-e2e-provider") {
		t.Fatal("provider should be registered after RegisterProvider()")
	}

	UnregisterProvider("test-e2e-provider")
	if IsProviderRegistered("test-e2e-provider") {
		t.Error("provider should be unregistered after UnregisterProvider()")
	}
}

func TestRegisterProviderPanicOnDuplicate(t *testing.T) {
	registryLock.Lock()
	delete(registry, "test-panic-provider")
	registryLock.Unlock()

	RegisterProvider("test-panic-provider", func(cfg Config) (Provider, error) {
		return nil, nil
	})

	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic on duplicate registration")
		}
		registryLock.Lock()
		delete(registry, "test-panic-provider")
		registryLock.Unlock()
	}()

	RegisterProvider("test-panic-provider", func(cfg Config) (Provider, error) {
		return nil, nil
	})
}

func TestIsProviderRegistered_FalseForUnknown(t *testing.T) {
	if IsProviderRegistered("__nonexistent_test_provider__") {
		t.Error("IsProviderRegistered() should be false for unregistered provider")
	}
}

func TestAvailableProviders(t *testing.T) {
	available := AvailableProviders()
	if len(available) == 0 {
		t.Error("AvailableProviders() should return at least the built-in providers")
	}

	openaiFound := false
	for _, p := range available {
		if p == "openai" {
			openaiFound = true
			break
		}
	}
	if !openaiFound {
		t.Error("AvailableProviders() should include 'openai'")
	}
}

func TestGetProviderDisplayName(t *testing.T) {
	name := GetProviderDisplayName("openai")
	if name == "" {
		t.Error("GetProviderDisplayName() should return non-empty for known provider")
	}
}

func TestGetProviderDescription(t *testing.T) {
	desc := GetProviderDescription("openai")
	if desc == "" {
		t.Error("GetProviderDescription() should return non-empty for known provider")
	}
}

func TestGetDefaultModel(t *testing.T) {
	model := GetDefaultModel("openai")
	if model == "" {
		t.Error("GetDefaultModel() should return non-empty for known provider")
	}
}

func TestGetDefaultModelForUnknownProvider(t *testing.T) {
	model := GetDefaultModel("__nonexistent__")
	if model != "" {
		t.Errorf("GetDefaultModel() should return empty for unknown provider, got %q", model)
	}
}
