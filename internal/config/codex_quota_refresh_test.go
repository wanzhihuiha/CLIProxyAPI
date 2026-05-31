package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigBytes_CodexQuotaRefreshDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.CodexQuotaRefresh.Enabled {
		t.Fatal("CodexQuotaRefresh.Enabled = false, want default true")
	}
	if cfg.CodexQuotaRefresh.Interval != DefaultCodexQuotaRefreshInterval {
		t.Fatalf("CodexQuotaRefresh.Interval = %q, want %q", cfg.CodexQuotaRefresh.Interval, DefaultCodexQuotaRefreshInterval)
	}
	if cfg.CodexQuotaRefresh.MaxConcurrency != DefaultCodexQuotaRefreshConcurrency {
		t.Fatalf("CodexQuotaRefresh.MaxConcurrency = %d, want %d", cfg.CodexQuotaRefresh.MaxConcurrency, DefaultCodexQuotaRefreshConcurrency)
	}
}

func TestParseConfigBytes_CodexQuotaRefreshOverrides(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
codex-quota-refresh:
  enabled: false
  interval: "30m"
  max-concurrency: 4
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.CodexQuotaRefresh.Enabled {
		t.Fatal("CodexQuotaRefresh.Enabled = true, want false")
	}
	if cfg.CodexQuotaRefresh.Interval != "30m" {
		t.Fatalf("CodexQuotaRefresh.Interval = %q, want 30m", cfg.CodexQuotaRefresh.Interval)
	}
	if cfg.CodexQuotaRefresh.MaxConcurrency != 4 {
		t.Fatalf("CodexQuotaRefresh.MaxConcurrency = %d, want 4", cfg.CodexQuotaRefresh.MaxConcurrency)
	}
}

func TestLoadConfigOptional_CodexQuotaRefreshDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if !cfg.CodexQuotaRefresh.Enabled {
		t.Fatal("CodexQuotaRefresh.Enabled = false, want default true")
	}
	if cfg.CodexQuotaRefresh.Interval != DefaultCodexQuotaRefreshInterval {
		t.Fatalf("CodexQuotaRefresh.Interval = %q, want %q", cfg.CodexQuotaRefresh.Interval, DefaultCodexQuotaRefreshInterval)
	}
	if cfg.CodexQuotaRefresh.MaxConcurrency != DefaultCodexQuotaRefreshConcurrency {
		t.Fatalf("CodexQuotaRefresh.MaxConcurrency = %d, want %d", cfg.CodexQuotaRefresh.MaxConcurrency, DefaultCodexQuotaRefreshConcurrency)
	}
}
