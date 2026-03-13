package classify

import (
	"os"
	"testing"
)

func TestParseConfigFile(t *testing.T) {
	content := `# Classifier settings
auto-approve: green
auto-approve-network: known

known-hosts:
  - host: github.com
  - host: api.github.com
    path: "/repos/**"
    methods: [GET, HEAD]
  - host: proxy.golang.org
`
	tmp := t.TempDir()
	path := tmp + "/classifier.yaml"
	os.WriteFile(path, []byte(content), 0644)

	cfg := ParseConfigFile(path)
	if cfg.AutoApprove != "green" {
		t.Errorf("AutoApprove = %q, want %q", cfg.AutoApprove, "green")
	}
	if cfg.AutoApproveNetwork != "known" {
		t.Errorf("AutoApproveNetwork = %q, want %q", cfg.AutoApproveNetwork, "known")
	}

	if cfg.KnownHosts == nil {
		t.Fatal("KnownHosts should not be nil")
	}
	if len(cfg.KnownHosts.Dests) != 3 {
		t.Fatalf("got %d known hosts, want 3", len(cfg.KnownHosts.Dests))
	}
	if cfg.KnownHosts.Dests[0].Host != "github.com" {
		t.Errorf("first host = %q", cfg.KnownHosts.Dests[0].Host)
	}
	if cfg.KnownHosts.Dests[1].Path != "/repos/**" {
		t.Errorf("second host path = %q", cfg.KnownHosts.Dests[1].Path)
	}
}

func TestParseConfigFileDefaults(t *testing.T) {
	cfg := ParseConfigFile("/nonexistent/path")
	if cfg.AutoApprove != "never" {
		t.Errorf("AutoApprove = %q, want %q", cfg.AutoApprove, "never")
	}
	if cfg.AutoApproveNetwork != "never" {
		t.Errorf("AutoApproveNetwork = %q, want %q", cfg.AutoApproveNetwork, "never")
	}
	if cfg.KnownHosts != nil {
		t.Errorf("KnownHosts should be nil for missing file")
	}
}

func TestParseConfigFileNoKnownHosts(t *testing.T) {
	content := `auto-approve: yellow
auto-approve-network: any
`
	tmp := t.TempDir()
	path := tmp + "/classifier.yaml"
	os.WriteFile(path, []byte(content), 0644)

	cfg := ParseConfigFile(path)
	if cfg.AutoApprove != "yellow" {
		t.Errorf("AutoApprove = %q, want %q", cfg.AutoApprove, "yellow")
	}
	if cfg.AutoApproveNetwork != "any" {
		t.Errorf("AutoApproveNetwork = %q, want %q", cfg.AutoApproveNetwork, "any")
	}
	if cfg.KnownHosts != nil {
		t.Errorf("KnownHosts should be nil when not specified")
	}
}
