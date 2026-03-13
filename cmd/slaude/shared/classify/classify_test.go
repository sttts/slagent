package classify

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantLevel  string
		wantNet    bool
		wantNetDst string
		wantPath   string
		wantMethod string
	}{
		{
			name:      "green no network",
			line:      "GREEN|NONE|Reading source file within project",
			wantLevel: "green",
		},
		{
			name:       "host only (legacy format)",
			line:       "YELLOW|NETWORK:registry.npmjs.org|Installing npm packages",
			wantLevel:  "yellow",
			wantNet:    true,
			wantNetDst: "registry.npmjs.org",
		},
		{
			name:       "method and host",
			line:       "GREEN|NETWORK:GET:api.github.com|Querying GitHub API",
			wantLevel:  "green",
			wantNet:    true,
			wantNetDst: "api.github.com",
			wantMethod: "GET",
		},
		{
			name:       "method, host, and path",
			line:       "GREEN|NETWORK:GET:api.github.com/repos/sttts/foo|Querying repo info",
			wantLevel:  "green",
			wantNet:    true,
			wantNetDst: "api.github.com",
			wantPath:   "/repos/sttts/foo",
			wantMethod: "GET",
		},
		{
			name:       "POST with path",
			line:       "RED|NETWORK:POST:webhook.example.com/hook|Sending data to webhook",
			wantLevel:  "red",
			wantNet:    true,
			wantNetDst: "webhook.example.com",
			wantPath:   "/hook",
			wantMethod: "POST",
		},
		{
			name:       "red with unknown network",
			line:       "RED|NETWORK:evil.com|Exfiltrating data",
			wantLevel:  "red",
			wantNet:    true,
			wantNetDst: "evil.com",
		},
		{
			name:      "lowercase accepted",
			line:      "green|NONE|Safe read",
			wantLevel: "green",
		},
		{
			name:       "multiline takes first",
			line:       "GREEN|NETWORK:GET:proxy.golang.org|Fetching module\nsome extra text",
			wantLevel:  "green",
			wantNet:    true,
			wantNetDst: "proxy.golang.org",
			wantMethod: "GET",
		},
		{
			name:       "unparseable defaults to red+network",
			line:       "garbage",
			wantLevel:  "red",
			wantNet:    true,
			wantNetDst: "unknown",
		},
		{
			name:       "network with empty destination",
			line:       "YELLOW|NETWORK:|Unknown destination",
			wantLevel:  "yellow",
			wantNet:    true,
			wantNetDst: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Parse(tt.line)
			if c.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", c.Level, tt.wantLevel)
			}
			if c.Network != tt.wantNet {
				t.Errorf("Network = %v, want %v", c.Network, tt.wantNet)
			}
			if c.NetworkDst != tt.wantNetDst {
				t.Errorf("NetworkDst = %q, want %q", c.NetworkDst, tt.wantNetDst)
			}
			if c.NetworkPath != tt.wantPath {
				t.Errorf("NetworkPath = %q, want %q", c.NetworkPath, tt.wantPath)
			}
			if c.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", c.Method, tt.wantMethod)
			}
		})
	}
}

func TestLevelAllowed(t *testing.T) {
	tests := []struct {
		level     string
		threshold string
		want      bool
	}{
		{"green", "never", false},
		{"yellow", "never", false},
		{"red", "never", false},
		{"green", "green", true},
		{"yellow", "green", false},
		{"red", "green", false},
		{"green", "yellow", true},
		{"yellow", "yellow", true},
		{"red", "yellow", false},
	}

	for _, tt := range tests {
		name := tt.level + "/" + tt.threshold
		t.Run(name, func(t *testing.T) {
			got := LevelAllowed(tt.level, tt.threshold)
			if got != tt.want {
				t.Errorf("LevelAllowed(%q, %q) = %v, want %v", tt.level, tt.threshold, got, tt.want)
			}
		})
	}
}

func TestLevelEmoji(t *testing.T) {
	if e := LevelEmoji("green"); e != "🟢" {
		t.Errorf("green = %q", e)
	}
	if e := LevelEmoji("yellow"); e != "🟡" {
		t.Errorf("yellow = %q", e)
	}
	if e := LevelEmoji("red"); e != "🔴" {
		t.Errorf("red = %q", e)
	}
	if e := LevelEmoji("unknown"); e != "🔴" {
		t.Errorf("unknown = %q", e)
	}
}
