package management

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth)
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestAPICallWhamUsageUpdatesCodexAccountQuota(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "codex-auth", Provider: "codex", Metadata: map[string]any{"email": "user@example.com"}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	targetURL, errParse := url.Parse("https://chatgpt.com/backend-api/wham/usage?include=usage")
	if errParse != nil {
		t.Fatalf("parse url: %v", errParse)
	}

	h := &Handler{authManager: manager}
	changed := h.maybeUpdateCodexWhamUsageQuota(
		context.Background(),
		http.MethodGet,
		targetURL,
		auth,
		http.Header{"Chatgpt-Account-Id": {"acct-header"}},
		http.StatusOK,
		[]byte(`{"account_id":"acct-body","user_id":"user-body","email":"codex@example.com","rate_limit":{"primary_window":{"used_percent":25},"secondary_window":{"used_percent":40}}}`),
	)
	if !changed {
		t.Fatal("expected codex quota snapshot update")
	}

	updated, _ := manager.GetByID("codex-auth")
	if !updated.Quota.FiveHourRemainingKnown || updated.Quota.FiveHourRemainingPercent != 75 {
		t.Fatalf("5h quota = (%v, %v), want known 75", updated.Quota.FiveHourRemainingKnown, updated.Quota.FiveHourRemainingPercent)
	}
	if !updated.Quota.SevenDayRemainingKnown || updated.Quota.SevenDayRemainingPercent != 60 {
		t.Fatalf("7d quota = (%v, %v), want known 60", updated.Quota.SevenDayRemainingKnown, updated.Quota.SevenDayRemainingPercent)
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("model states changed: %#v", updated.ModelStates)
	}
	if got := updated.Metadata["codex_account_id"]; got != "acct-header" {
		t.Fatalf("codex_account_id = %#v, want acct-header", got)
	}
	if got := updated.Metadata["codex_user_id"]; got != "user-body" {
		t.Fatalf("codex_user_id = %#v, want user-body", got)
	}
	if got := updated.Metadata["email"]; got != "codex@example.com" {
		t.Fatalf("email = %#v, want codex@example.com", got)
	}
}

func TestAPICallWhamUsageSavesResponseIdentityWithoutHeader(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "codex-auth", Provider: "codex", Metadata: map[string]any{"type": "codex"}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	targetURL, errParse := url.Parse("https://chatgpt.com/backend-api/wham/usage")
	if errParse != nil {
		t.Fatalf("parse url: %v", errParse)
	}

	h := &Handler{authManager: manager}
	changed := h.maybeUpdateCodexWhamUsageQuota(
		context.Background(),
		http.MethodGet,
		targetURL,
		auth,
		nil,
		http.StatusOK,
		[]byte(`{"account":{"account_id":"acct-body","user_id":"user-body","email":"codex@example.com"},"rate_limit":{"primary_window":{"used_percent":30}}}`),
	)
	if !changed {
		t.Fatal("expected codex quota snapshot update")
	}

	updated, _ := manager.GetByID("codex-auth")
	if got := updated.Metadata["codex_account_id"]; got != "acct-body" {
		t.Fatalf("codex_account_id = %#v, want acct-body", got)
	}
	if got := updated.Metadata["codex_user_id"]; got != "user-body" {
		t.Fatalf("codex_user_id = %#v, want user-body", got)
	}
	if got := updated.Metadata["email"]; got != "codex@example.com" {
		t.Fatalf("email = %#v, want codex@example.com", got)
	}
}

func TestAPICallWhamUsageSkipsNonMatchingRequests(t *testing.T) {
	t.Parallel()

	validURL, errParse := url.Parse("https://chatgpt.com/backend-api/wham/usage")
	if errParse != nil {
		t.Fatalf("parse valid url: %v", errParse)
	}
	otherPathURL, errParse := url.Parse("https://chatgpt.com/backend-api/other")
	if errParse != nil {
		t.Fatalf("parse other path url: %v", errParse)
	}

	tests := []struct {
		name       string
		method     string
		targetURL  *url.URL
		provider   string
		statusCode int
		body       []byte
	}{
		{
			name:       "non get method",
			method:     http.MethodPost,
			targetURL:  validURL,
			provider:   "codex",
			statusCode: http.StatusOK,
			body:       []byte(`{"rate_limit":{"primary_window":{"used_percent":25}}}`),
		},
		{
			name:       "non wham path",
			method:     http.MethodGet,
			targetURL:  otherPathURL,
			provider:   "codex",
			statusCode: http.StatusOK,
			body:       []byte(`{"rate_limit":{"primary_window":{"used_percent":25}}}`),
		},
		{
			name:       "non codex auth",
			method:     http.MethodGet,
			targetURL:  validURL,
			provider:   "gemini",
			statusCode: http.StatusOK,
			body:       []byte(`{"rate_limit":{"primary_window":{"used_percent":25}}}`),
		},
		{
			name:       "non ok status",
			method:     http.MethodGet,
			targetURL:  validURL,
			provider:   "codex",
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"rate_limit":{"primary_window":{"used_percent":25}}}`),
		},
		{
			name:       "parser rejected",
			method:     http.MethodGet,
			targetURL:  validURL,
			provider:   "codex",
			statusCode: http.StatusOK,
			body:       []byte(`{"rate_limit":{"primary_window":{"used_percent":"25"}}}`),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manager := coreauth.NewManager(nil, nil, nil)
			auth := &coreauth.Auth{ID: "auth-a", Provider: tc.provider, Metadata: map[string]any{"email": "user@example.com"}}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			h := &Handler{authManager: manager}

			if changed := h.maybeUpdateCodexWhamUsageQuota(context.Background(), tc.method, tc.targetURL, auth, nil, tc.statusCode, tc.body); changed {
				t.Fatal("did not expect codex quota snapshot update")
			}
			updated, _ := manager.GetByID("auth-a")
			if updated.Quota.FiveHourRemainingKnown || updated.Quota.SevenDayRemainingKnown {
				t.Fatalf("quota changed: %#v", updated.Quota)
			}
		})
	}
}
