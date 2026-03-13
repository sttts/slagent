package classify

import (
	"bufio"
	"os"
	"strings"
)

// KnownDest is a known-safe network destination.
type KnownDest struct {
	Host    string          // exact host or glob pattern (e.g. "*.github.com")
	Path    string          // optional URL path glob (e.g. "/repos/**"); empty = any path
	Methods map[string]bool // optional allowed HTTP methods (e.g. GET, HEAD); nil = any method
}

// KnownHostSet holds known-safe network destinations for auto-approve.
type KnownHostSet struct {
	Dests []KnownDest
}

// Match returns true if host matches a known destination (any path, any method).
func (k *KnownHostSet) Match(host string) bool {
	return k.MatchRequest(host, "", "")
}

// MatchRequest returns true if host + URL path + method matches a known destination.
// Empty urlPath or method means "any".
func (k *KnownHostSet) MatchRequest(host, urlPath, method string) bool {
	for _, d := range k.Dests {
		if !matchHostPattern(d.Host, host) && d.Host != host {
			continue
		}
		if d.Path != "" && (urlPath == "" || !matchPathPattern(d.Path, urlPath)) {
			continue
		}
		if d.Methods != nil {
			if method == "" || !d.Methods[strings.ToUpper(method)] {
				continue
			}
		}
		return true
	}
	return false
}

// matchHostPattern matches a host against a DNS-aware glob pattern.
//   - "*" matches exactly one DNS label (no dots)
//   - "**" matches one or more DNS labels
func matchHostPattern(pattern, host string) bool {
	return matchParts(strings.Split(pattern, "."), strings.Split(host, "."))
}

// matchParts recursively matches pattern parts against host parts.
func matchParts(pparts, hparts []string) bool {
	for len(pparts) > 0 && len(hparts) > 0 {
		p := pparts[0]
		if p == "**" {
			rest := pparts[1:]
			for i := 1; i <= len(hparts)-len(rest); i++ {
				if matchParts(rest, hparts[i:]) {
					return true
				}
			}
			return false
		}
		if p != "*" && p != hparts[0] {
			return false
		}
		pparts = pparts[1:]
		hparts = hparts[1:]
	}
	return len(pparts) == 0 && len(hparts) == 0
}

// matchPathPattern matches a URL path against a glob pattern using "/" as separator.
func matchPathPattern(pattern, urlPath string) bool {
	return matchParts(splitPath(pattern), splitPath(urlPath))
}

// splitPath splits a path into segments, stripping leading/trailing slashes.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// Add adds an exact host to the set.
func (k *KnownHostSet) Add(host string) {
	k.Dests = append(k.Dests, KnownDest{Host: host})
}

// ReadOnly is the default method set: GET and HEAD only.
var ReadOnly = map[string]bool{"GET": true, "HEAD": true}

// DefaultKnownDests are used when no config file provides known-hosts.
var DefaultKnownDests = []KnownDest{
	{Host: "github.com", Methods: ReadOnly},
	{Host: "api.github.com", Methods: ReadOnly},
	{Host: "raw.githubusercontent.com", Methods: ReadOnly},
	{Host: "proxy.golang.org", Methods: ReadOnly},
	{Host: "sum.golang.org", Methods: ReadOnly},
	{Host: "registry.npmjs.org", Methods: ReadOnly},
	{Host: "pypi.org", Methods: ReadOnly},
	{Host: "files.pythonhosted.org", Methods: ReadOnly},
	{Host: "rubygems.org", Methods: ReadOnly},
	{Host: "crates.io", Methods: ReadOnly},
	{Host: "static.crates.io", Methods: ReadOnly},
}

// LoadKnownHosts loads the known host set from the config file,
// falling back to built-in defaults.
func LoadKnownHosts() *KnownHostSet {
	cfg := LoadConfig()
	if cfg.KnownHosts != nil {
		return cfg.KnownHosts
	}
	return &KnownHostSet{Dests: append([]KnownDest{}, DefaultKnownDests...)}
}

// ParseKnownHostsEntries reads known-hosts entries from a YAML file.
func ParseKnownHostsEntries(filePath string) ([]KnownDest, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return parseKnownHostsFromScanner(bufio.NewScanner(f))
}

// parseKnownHostsFromScanner parses known-hosts entries from a scanner.
func parseKnownHostsFromScanner(scanner *bufio.Scanner) ([]KnownDest, error) {
	var dests []KnownDest
	var current *KnownDest
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "- host:") {
			if current != nil {
				if current.Methods == nil {
					current.Methods = map[string]bool{"GET": true, "HEAD": true}
				}
				dests = append(dests, *current)
			}
			value := Unquote(strings.TrimSpace(strings.TrimPrefix(line, "- host:")))
			if value != "" {
				current = &KnownDest{Host: value}
			} else {
				current = nil
			}
			continue
		}

		if strings.HasPrefix(line, "path:") && current != nil {
			current.Path = Unquote(strings.TrimSpace(strings.TrimPrefix(line, "path:")))
			continue
		}

		if strings.HasPrefix(line, "methods:") && current != nil {
			raw := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "methods:")), "[]")
			current.Methods = make(map[string]bool)
			for _, m := range strings.Split(raw, ",") {
				m = strings.TrimSpace(m)
				if m != "" {
					current.Methods[strings.ToUpper(m)] = true
				}
			}
			continue
		}
	}

	if current != nil {
		if current.Methods == nil {
			current.Methods = map[string]bool{"GET": true, "HEAD": true}
		}
		dests = append(dests, *current)
	}
	return dests, scanner.Err()
}

// Unquote strips surrounding single or double quotes from a YAML value.
func Unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
