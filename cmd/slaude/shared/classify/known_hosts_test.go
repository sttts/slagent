package classify

import (
	"os"
	"testing"
)

func TestParseKnownHostsEntries(t *testing.T) {
	content := `# Package managers
- host: github.com
- host: api.github.com
  path: "/repos/**"
  methods: [GET, HEAD]
- host: "*.googleapis.com"
- host: '*.cdn.example.com'

# Comment
- host: pypi.org
- invalid line
- host:
`
	tmp := t.TempDir()
	path := tmp + "/known-hosts.yaml"
	os.WriteFile(path, []byte(content), 0644)

	dests, err := ParseKnownHostsEntries(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(dests) != 5 {
		t.Fatalf("got %d dests, want 5: %+v", len(dests), dests)
	}

	// github.com — host only (defaults to GET+HEAD)
	if dests[0].Host != "github.com" || dests[0].Path != "" {
		t.Errorf("dests[0] = %+v", dests[0])
	}
	if !dests[0].Methods["GET"] || !dests[0].Methods["HEAD"] || len(dests[0].Methods) != 2 {
		t.Errorf("dests[0] methods should default to GET+HEAD, got %v", dests[0].Methods)
	}

	// api.github.com with path and methods
	if dests[1].Host != "api.github.com" || dests[1].Path != "/repos/**" {
		t.Errorf("dests[1] host/path = %+v", dests[1])
	}
	if !dests[1].Methods["GET"] || !dests[1].Methods["HEAD"] || len(dests[1].Methods) != 2 {
		t.Errorf("dests[1] methods = %v", dests[1].Methods)
	}

	// Glob hosts
	if dests[2].Host != "*.googleapis.com" {
		t.Errorf("dests[2] = %+v", dests[2])
	}
	if dests[3].Host != "*.cdn.example.com" {
		t.Errorf("dests[3] = %+v", dests[3])
	}

	// pypi.org
	if dests[4].Host != "pypi.org" {
		t.Errorf("dests[4] = %+v", dests[4])
	}
}

func TestKnownHostSetMatch(t *testing.T) {
	set := &KnownHostSet{Dests: []KnownDest{
		{Host: "github.com"},
		{Host: "api.github.com"},
		{Host: "*.googleapis.com"},
		{Host: "**.cdn.example.com"},
	}}

	tests := []struct {
		host string
		want bool
	}{
		{"github.com", true},
		{"api.github.com", true},
		{"evil.github.com", false},
		{"storage.googleapis.com", true},
		{"googleapis.com", false},
		{"evil.com", false},

		// * matches one label only
		{"a.b.googleapis.com", false},

		// ** matches one or more labels
		{"us.cdn.example.com", true},
		{"us.east.cdn.example.com", true},
		{"cdn.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := set.Match(tt.host); got != tt.want {
				t.Errorf("Match(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestKnownHostSetMatchRequest(t *testing.T) {
	set := &KnownHostSet{Dests: []KnownDest{
		{Host: "github.com"},
		{Host: "api.github.com", Path: "/repos/**", Methods: map[string]bool{"GET": true, "HEAD": true}},
		{Host: "uploads.example.com", Path: "/files/*"},
	}}

	tests := []struct {
		host, path, method string
		want               bool
	}{
		// Host-only match
		{"github.com", "/anything", "POST", true},

		// Path + method restricted
		{"api.github.com", "/repos/foo/bar", "GET", true},
		{"api.github.com", "/repos/foo/bar", "HEAD", true},
		{"api.github.com", "/repos/foo/bar", "DELETE", false},
		{"api.github.com", "/users/foo", "GET", false},

		// Path-only restriction (no method filter)
		{"uploads.example.com", "/files/image.png", "", true},
		{"uploads.example.com", "/files/a/b.png", "", false},
		{"uploads.example.com", "/other/path", "", false},
	}

	for _, tt := range tests {
		name := tt.host + tt.path + ":" + tt.method
		t.Run(name, func(t *testing.T) {
			if got := set.MatchRequest(tt.host, tt.path, tt.method); got != tt.want {
				t.Errorf("MatchRequest(%q, %q, %q) = %v, want %v", tt.host, tt.path, tt.method, got, tt.want)
			}
		})
	}
}

func TestMatchHostPattern(t *testing.T) {
	tests := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"github.com", "github.com", true},
		{"github.com", "api.github.com", false},
		{"*.github.com", "api.github.com", true},
		{"*.github.com", "a.b.github.com", false},
		{"*.github.com", "github.com", false},
		{"**.github.com", "api.github.com", true},
		{"**.github.com", "a.b.github.com", true},
		{"**.github.com", "a.b.c.github.com", true},
		{"**.github.com", "github.com", false},
		{"cdn.*.example.com", "cdn.us.example.com", true},
		{"cdn.*.example.com", "cdn.us.east.example.com", false},
		{"cdn.**.example.com", "cdn.us.east.example.com", true},
	}

	for _, tt := range tests {
		name := tt.pattern + "/" + tt.host
		t.Run(name, func(t *testing.T) {
			if got := matchHostPattern(tt.pattern, tt.host); got != tt.want {
				t.Errorf("matchHostPattern(%q, %q) = %v, want %v", tt.pattern, tt.host, got, tt.want)
			}
		})
	}
}

func TestMatchPathPattern(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"/repos/**", "/repos/foo/bar", true},
		{"/repos/**", "/repos/foo", true},
		{"/repos/**", "/repos", false},
		{"/repos/*", "/repos/foo", true},
		{"/repos/*", "/repos/foo/bar", false},
	}

	for _, tt := range tests {
		name := tt.pattern + ":" + tt.path
		t.Run(name, func(t *testing.T) {
			if got := matchPathPattern(tt.pattern, tt.path); got != tt.want {
				t.Errorf("matchPathPattern(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestKnownHostSetAdd(t *testing.T) {
	set := &KnownHostSet{}
	set.Add("new.example.com")
	if !set.Match("new.example.com") {
		t.Error("added host should match")
	}
}

func TestLoadKnownHostsDefaults(t *testing.T) {
	set := LoadKnownHosts()

	// Defaults require GET/HEAD method
	if !set.MatchRequest("github.com", "", "GET") {
		t.Error("defaults should include github.com GET")
	}
	if !set.MatchRequest("proxy.golang.org", "", "HEAD") {
		t.Error("defaults should include proxy.golang.org HEAD")
	}
	if set.MatchRequest("github.com", "", "POST") {
		t.Error("defaults should not allow github.com POST")
	}
	if set.MatchRequest("evil.com", "", "GET") {
		t.Error("defaults should not include evil.com")
	}

	// Match() without method should not match method-restricted entries
	if set.Match("github.com") {
		t.Error("Match() without method should not match method-restricted defaults")
	}
}
