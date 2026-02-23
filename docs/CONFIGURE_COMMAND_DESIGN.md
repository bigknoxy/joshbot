# joshbot configure Command Design

## 1. User Flow Diagram

### Command Structure

```
joshbot configure              # Interactive wizard (all steps)
joshbot configure --list        # Show configured providers
joshbot configure --add <prov>  # Add specific provider (interactive)
joshbot configure --remove <prov>  # Remove a provider
joshbot configure --default <prov> # Set default provider
joshbot configure --fallback   # Configure fallback order
```

### Interactive Wizard Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                    joshbot configure                           │
│                    (Interactive Wizard)                        │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  [Step 1] Current Configuration                                │
│  ─────────────────────────────────────                          │
│  Configured Providers:                                          │
│    ✓ openrouter  (default)  sk-or-v1-abc****1234              │
│    ✗ groq        (fallback) gsk_****wxyz                       │
│    ✗ ollama     (fallback) http://localhost:11434              │
│                                                                 │
│  [1] Add a new provider                                         │
│  [2] Remove a provider                                          │
│  [3] Set default provider                                       │
│  [4] Configure fallback order                                   │
│  [5] Done                                                       │
└─────────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
        ┌──────────┐   ┌──────────┐   ┌──────────┐
        │Add       │   │Remove    │   │Set       │
        │Provider  │   │Provider  │   │Default   │
        └──────────┘   └──────────┘   └──────────┘
              │               │               │
              ▼               ▼               ▼
┌─────────────────────────────────────────────────────────────────┐
│  [Step 2] Add Provider                                         │
│  ─────────────────────────────────────                          │
│  Available Providers:                                           │
│    [1] OpenRouter  - Use OpenAI-compatible APIs via OpenRouter │
│    [2] Groq       - Fast inference with Groq API               │
│    [3] Ollama    - Self-hosted models via Ollama                │
│    [4] OpenAI    - Direct OpenAI API access                     │
│    [5] Anthropic - Claude models via Anthropic API              │
│                                                                 │
│  Choose provider [1-5]: _                                       │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  [Step 3] Provider Configuration                               │
│  ─────────────────────────────────────                          │
│                                                                 │
│  (For OpenRouter/Groq/OpenAI/Anthropic)                        │
│  ════════════════════════════════════════                      │
│  Enter API key: sk-or-v1-________________________               │
│  (Get from: https://provider.example.com/keys)                 │
│                                                                 │
│  ─ OR ─                                                        │
│                                                                 │
│  (For Ollama)                                                  │
│  ════════════════════════════════════════                      │
│  Base URL [http://localhost:11434]: _                           │
│                                                                 │
│  Models available on this provider:                             │
│    1. llama3.1:8b                                              │
│    2. mistral:7b                                                │
│    3. codellama:7b                                              │
│  Select default model [1]: _                                    │
│                                                                 │
│  [S] Skip validation                                           │
│  [V] Validate credentials now (recommended)                    │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼ (if validation selected)
┌─────────────────────────────────────────────────────────────────┐
│  [Step 4] Validation                                           │
│  ─────────────────────────────────────                          │
│  Testing connection...                                          │
│  ✓ Connection successful!                                       │
│  ✓ Models retrieved: 3 available                               │
│                                                                 │
│  Would you like to test a quick chat? [y/N]: _                 │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  [Step 5] Set Default / Fallback                               │
│  ─────────────────────────────────────                          │
│                                                                 │
│  Available providers:                                          │
│    1. openrouter (newly added)                                 │
│                                                                 │
│  Use as:                                                       │
│    [1] Default provider (primary)                               │
│    [2] Fallback only                                           │
│    [3] Both (default + fallback)                                │
│                                                                 │
│  Note: If you have 2+ providers, you can configure            │
│        fallback order after adding all providers.              │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│  [Step 6] Confirm & Save                                       │
│  ─────────────────────────────────────                          │
│                                                                 │
│  Summary:                                                      │
│    Provider:  Groq                                             │
│    API Key:   gsk_****wxyz (masked)                            │
│    Default:   Yes                                              │
│                                                                 │
│  [1] Save and continue                                         │
│  [2] Save and exit                                             │
│  [3] Cancel                                                    │
└─────────────────────────────────────────────────────────────────┘
```

### Fallback Configuration Flow (when 2+ providers)

```
┌─────────────────────────────────────────────────────────────────┐
│  Configure Fallback Order                                      │
│  ─────────────────────────────────────                          │
│                                                                 │
│  Current fallback chain:                                       │
│    1. openrouter  (default)                                     │
│    2. groq        (fallback #1)                                │
│    3. ollama     (fallback #2)                                 │
│                                                                 │
│  [1] Reorder fallbacks                                         │
│  [2] Test fallback chain                                        │
│  [3] Done                                                      │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼ (if reorder selected)
┌─────────────────────────────────────────────────────────────────┐
│  Reorder Fallbacks                                             │
│  ─────────────────────────────────────                          │
│                                                                 │
│  Drag to reorder (or enter numbers):                           │
│    [1] openrouter  ████████████████████ (primary)              │
│    [2] groq        ████████████ (fallback #1)                  │
│    [3] ollama     ████████ (fallback #2)                       │
│                                                                 │
│  Enter new order (e.g., "3,1,2"): _                            │
└─────────────────────────────────────────────────────────────────┘
```

## 2. Config Schema Design

### Current Schema (v1)

```json
{
  "schema_version": 1,
  "providers": {
    "openrouter": {
      "api_key": "",
      "api_base": "",
      "extra_headers": {}
    }
  },
  ...
}
```

### Proposed Schema (v2)

```json
{
  "schema_version": 2,
  "providers": {
    "openrouter": {
      "type": "openrouter",
      "api_key": "sk-or-v1-...",
      "api_base": "",
      "extra_headers": {},
      "enabled": true,
      "models": ["default"]
    },
    "groq": {
      "type": "groq",
      "api_key": "gsk_...",
      "api_base": "",
      "enabled": true
    },
    "ollama": {
      "type": "ollama",
      "api_base": "http://localhost:11434",
      "enabled": true
    }
  },
  "provider_defaults": {
    "default": "openrouter",
    "fallback_order": ["groq", "ollama"]
  },
  "agents": {
    "defaults": {
      "model": "z-ai/glm-4.5-air:free"
    }
  }
}
```

### Schema Changes

1. **Add `type` field** to each provider (for clarity, allows renaming)
2. **Add `enabled` field** to each provider (soft delete without removing config)
3. **Add `models` field** to store available/cached models per provider
4. **Add `provider_defaults` section**:
   - `default`: Primary provider name
   - `fallback_order`: Array of provider names in fallback priority

### Config Code Changes

```go
// internal/config/config.go

// ProviderConfig - enhanced with type and enabled status
type ProviderConfig struct {
    Type         string            `mapstructure:"type" json:"type" yaml:"type"`
    APIKey       string            `mapstructure:"api_key" json:"api_key" yaml:"api_key"`
    APIBase      string            `mapstructure:"api_base" json:"api_base" yaml:"api_base"`
    ExtraHeaders map[string]string `mapstructure:"extra_headers" json:"extra_headers" yaml:"extra_headers"`
    Enabled      bool              `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
    Models       []string          `mapstructure:"models" json:"models" yaml:"models"`
}

// ProviderDefaults holds default provider settings
type ProviderDefaults struct {
    Default      string   `mapstructure:"default" json:"default" yaml:"default"`
    FallbackOrder []string `mapstructure:"fallback_order" json:"fallback_order" yaml:"fallback_order"`
}

// Config - root config updated
type Config struct {
    SchemaVersion    int                       `mapstructure:"schema_version" json:"schema_version"`
    Providers        map[string]ProviderConfig `mapstructure:"providers" json:"providers" yaml:"providers"`
    ProviderDefaults ProviderDefaults         `mapstructure:"provider_defaults" json:"provider_defaults" yaml:"provider_defaults"`
    // ... rest unchanged
}
```

## 3. Implementation Checklist

### Phase 1: Schema & Config Changes

- [ ] Update config schema version to 2
- [ ] Add `ProviderDefaults` struct
- [ ] Update `ProviderConfig` with new fields
- [ ] Add migration function for v1 → v2
- [ ] Update `Validate()` to check provider_defaults
- [ ] Add helper methods: `GetDefaultProvider()`, `GetFallbackProviders()`

### Phase 2: Provider Registry Enhancements

- [ ] Add Groq provider registration (if not present)
- [ ] Add `ValidateCredentials()` method to providers
- [ ] Add `ListModels()` method to providers
- [ ] Add `TestConnection()` helper

### Phase 3: CLI Command Implementation

- [ ] Create `internal/configure/wizard.go` - interactive wizard logic
- [ ] Add `configure` command to main.go with subcommands:
  - [ ] `configure` - run wizard
  - [ ] `configure --list` - show providers
  - [ ] `configure --add <provider>` - add specific
  - [ ] `configure --remove <provider>` - remove
  - [ ] `configure --default <provider>` - set default
  - [ ] `configure --fallback` - configure fallback
- [ ] Implement masking for API key display
- [ ] Add input validation and sanitization

### Phase 4: Onboard Integration

- [ ] Update onboard wizard to use new provider flow
- [ ] Offer to configure multiple providers during onboard
- [ ] Set fallback order during onboard (if 2+ providers)

### Phase 5: Provider Switching Logic

- [ ] Update `setupComponents()` in main.go to use fallback chain
- [ ] Add provider health check on startup
- [ ] Implement automatic fallback on provider failure

## 4. Edge Cases to Handle

### Configuration Edge Cases

1. **No providers configured**: Show helpful message, redirect to onboard or configure
2. **All providers disabled**: Warn user, suggest enabling or adding
3. **Default provider doesn't exist**: Auto-fallback to first available
4. **Fallback order has invalid provider**: Remove from order, warn user
5. **Circular fallback**: Detect and prevent (shouldn't happen with current design)

### Input Validation

1. **Empty API key**: Require at least 1 character, warn about empty keys
2. **Invalid URL for Ollama**: Validate URL format, test connection
3. **Duplicate provider**: Ask to update existing or use different name
4. **API key with whitespace**: Auto-trim, warn user

### Provider-Specific

1. **OpenRouter**: Require valid API key format (sk-or-v1-...)
2. **Groq**: Require gsk_ prefix
3. **Ollama**: Validate base URL, test /api/tags endpoint

### UX Edge Cases

1. **Ctrl+C during prompts**: Clean exit, don't save partial config
2. **Terminal too narrow**: Use simpler output format
3. **Non-interactive mode**: Support flags for scripted setup
4. **API key in environment variable**: Detect and use instead of prompting

### Error Handling

1. **Provider API timeout**: Show helpful error, offer retry
2. **Invalid credentials**: Clear error message, allow re-entry
3. **Config file corrupted**: Offer to reset or backup
4. **Permission denied on save**: Clear error with fix instructions

## 5. Provider API Endpoints

| Provider | API Base | Auth | Models Endpoint |
|----------|----------|------|-----------------|
| OpenRouter | https://openrouter.ai/api/v1 | Bearer token | /models |
| Groq | https://api.groq.com/openai/v1 | Bearer token | /models |
| Ollama | http://localhost:11434 | None | /api/tags |
| OpenAI | https://api.openai.com/v1 | Bearer token | /models |
| Anthropic | https://api.anthropic.com/v1 | Bearer token (header) | N/A (list via web) |

## 6. File Structure

```
internal/
├── configure/
│   ├── wizard.go        # Interactive wizard logic
│   ├── prompts.go       # Input prompts and validation
│   ├── providers.go     # Provider-specific configuration
│   └── test.go         # Connection testing utilities
cmd/
└── joshbot/
    └── main.go          # Add configure command
```

## 7. Quick Reference: Provider Identifiers

```go
const (
    ProviderOpenRouter = "openrouter"
    ProviderGroq       = "groq"
    ProviderOllama     = "ollama"
    ProviderOpenAI     = "openai"
    ProviderAnthropic  = "anthropic"
)
```
