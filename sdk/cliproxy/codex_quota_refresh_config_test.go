package cliproxy

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestCodexQuotaRefreshOptionsFromConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.CodexQuotaRefresh.Enabled = true
	cfg.CodexQuotaRefresh.Interval = "30m"
	cfg.CodexQuotaRefresh.MaxConcurrency = 4

	opts := codexQuotaRefreshOptionsFromConfig(cfg)
	if !opts.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if opts.Interval != 30*time.Minute {
		t.Fatalf("Interval = %s, want 30m", opts.Interval)
	}
	if opts.MaxConcurrency != 4 {
		t.Fatalf("MaxConcurrency = %d, want 4", opts.MaxConcurrency)
	}
}

func TestCodexQuotaRefreshOptionsFromConfigFallbacks(t *testing.T) {
	cfg := &config.Config{}
	cfg.CodexQuotaRefresh.Enabled = true
	cfg.CodexQuotaRefresh.Interval = "bad"
	cfg.CodexQuotaRefresh.MaxConcurrency = -1

	opts := codexQuotaRefreshOptionsFromConfig(cfg)
	if opts.Interval != 10*time.Minute {
		t.Fatalf("Interval = %s, want 10m fallback", opts.Interval)
	}
	if opts.MaxConcurrency != config.DefaultCodexQuotaRefreshConcurrency {
		t.Fatalf("MaxConcurrency = %d, want default", opts.MaxConcurrency)
	}
}
