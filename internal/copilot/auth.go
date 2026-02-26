package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bigknoxy/joshbot/internal/log"
)

const (
	ClientID       = "Ov23liNV83W9jiYnzBdK"
	DeviceCodeURL  = "https://github.com/login/device/code"
	AccessTokenURL = "https://github.com/login/oauth/access_token"
	CopilotAPIURL  = "https://api.githubcopilot.com/v1"
)

var Version = "dev"

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

type TokenInfo struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

type AuthData map[string]TokenInfo

func InitiateDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", ClientID)
	data.Set("scope", "read:user")

	req, err := http.NewRequestWithContext(ctx, "POST", DeviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "joshbot/"+Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to initiate device flow: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device flow initiation failed with status: %d", resp.StatusCode)
	}

	var result DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode device code response: %w", err)
	}

	return &result, nil
}

func PollForToken(ctx context.Context, deviceCode string, intervalSec int) (*TokenInfo, error) {
	if intervalSec < 5 {
		intervalSec = 5
	}

	log.Debug("starting token poll with interval: %d seconds", intervalSec)

	client := &http.Client{Timeout: 60 * time.Second}

	// Immediate check before starting ticker
	log.Debug("attempting immediate token exchange...")
	fmt.Print(".")
	token, suggestedInterval, err := attemptTokenExchange(ctx, client, deviceCode)
	if err != nil {
		if isAuthError(err) {
			return nil, err
		}
		// Show network errors to user for debugging
		fmt.Printf("\n[DEBUG] Token check error: %v (will retry)\n", err)
		log.Debug("token poll error (initial): %v", err)
	}
	if token != nil {
		log.Debug("token received on initial attempt")
		fmt.Println(" authorized!")
		return token, nil
	}

	// If GitHub suggested a slower interval, use it
	if suggestedInterval > intervalSec {
		intervalSec = suggestedInterval
		fmt.Printf("[DEBUG] Using GitHub suggested interval: %ds\n", intervalSec)
	}

	log.Debug("authorization pending, starting poll ticker...")
	fmt.Print("Waiting for authorization")

	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			fmt.Print(".")
			log.Debug("polling for token...")
			token, suggestedInterval, err := attemptTokenExchange(ctx, client, deviceCode)
			if err != nil {
				if isAuthError(err) {
					log.Debug("auth error, stopping poll: %v", err)
					return nil, err
				}
				// Show network errors to user for debugging
				fmt.Printf("\nWarning: %v (will retry)\n", err)
				log.Debug("token poll error, retrying: %v", err)
				continue
			}
			if token != nil {
				log.Debug("token received!")
				fmt.Println(" authorized!")
				return token, nil
			}
			// Update interval if GitHub suggested a slower rate
			if suggestedInterval > intervalSec {
				intervalSec = suggestedInterval
				fmt.Printf("\n[DEBUG] Slowing down to %ds interval per GitHub\n", intervalSec)
				ticker.Stop()
				ticker = time.NewTicker(time.Duration(intervalSec) * time.Second)
			}
			log.Debug("authorization still pending")
		}
	}
}

func attemptTokenExchange(ctx context.Context, client *http.Client, deviceCode string) (*TokenInfo, int, error) {
	data := url.Values{}
	data.Set("client_id", ClientID)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, "POST", AccessTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "joshbot/"+Version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	// Always show response status for debugging
	fmt.Printf("[DEBUG] Token response: status=%d body=%s\n", resp.StatusCode, string(body))

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
		Interval     int    `json:"interval"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, 0, fmt.Errorf("failed to decode token response: %w", err)
	}

	if result.Error != "" {
		switch result.Error {
		case "authorization_pending":
			return nil, 0, nil
		case "slow_down":
			// Return the interval from slow_down so caller can adjust polling
			if result.Interval > 0 {
				fmt.Printf("[DEBUG] GitHub requests slower polling: interval=%ds\n", result.Interval)
			}
			return nil, result.Interval, nil
		case "expired_token":
			return nil, 0, fmt.Errorf("authorization expired, please run auth again")
		case "access_denied":
			return nil, 0, fmt.Errorf("authorization denied")
		default:
			return nil, 0, fmt.Errorf("auth error: %s - %s", result.Error, result.ErrorDesc)
		}
	}

	if result.AccessToken == "" {
		return nil, 0, nil
	}

	expiresAt := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Unix()
	return &TokenInfo{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    expiresAt,
	}, 0, nil
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "expired") ||
		strings.Contains(errStr, "denied") ||
		strings.Contains(errStr, "run auth again")
}

func LoadToken(homeDir string) (*TokenInfo, error) {
	authFile := filepath.Join(homeDir, ".joshbot", "auth.json")

	data, err := os.ReadFile(authFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read auth file: %w", err)
	}

	var authData AuthData
	if err := json.Unmarshal(data, &authData); err != nil {
		return nil, fmt.Errorf("failed to parse auth file: %w", err)
	}

	token, ok := authData["github-copilot"]
	if !ok {
		return nil, nil
	}

	if token.ExpiresAt > 0 && time.Now().Unix() > token.ExpiresAt {
		return nil, fmt.Errorf("token expired, please re-authenticate")
	}

	return &token, nil
}

func SaveToken(homeDir string, info *TokenInfo) error {
	authDir := filepath.Join(homeDir, ".joshbot")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	authFile := filepath.Join(authDir, "auth.json")

	var authData AuthData
	data, err := os.ReadFile(authFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read existing auth file: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &authData); err != nil {
			return fmt.Errorf("failed to parse existing auth file: %w", err)
		}
	}

	if authData == nil {
		authData = make(AuthData)
	}
	authData["github-copilot"] = *info

	newData, err := json.MarshalIndent(authData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal auth data: %w", err)
	}

	if err := os.WriteFile(authFile, newData, 0600); err != nil {
		return fmt.Errorf("failed to write auth file: %w", err)
	}

	return nil
}

func RunDeviceFlow(ctx context.Context) (*TokenInfo, error) {
	deviceCode, err := InitiateDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initiate device flow: %w", err)
	}

	fmt.Printf("\nPlease visit: %s\n", deviceCode.VerificationURI)
	fmt.Printf("Enter code: %s\n\n", deviceCode.UserCode)
	fmt.Println("Waiting for authorization...")

	token, err := PollForToken(ctx, deviceCode.DeviceCode, deviceCode.Interval)
	if err != nil {
		return nil, fmt.Errorf("token polling failed: %w", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	if err := SaveToken(homeDir, token); err != nil {
		return nil, fmt.Errorf("failed to save token: %w", err)
	}

	log.Info("GitHub Copilot authentication successful")
	return token, nil
}
