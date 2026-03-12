package session

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// knownDest is a known-safe network destination.
type knownDest struct {
	Host    string          // exact host or glob pattern (e.g. "*.github.com")
	Path    string          // optional URL path glob (e.g. "/repos/**"); empty = any path
	Methods map[string]bool // optional allowed HTTP methods (e.g. GET, HEAD); nil = any method
}

// knownHostSet holds known-safe network destinations for auto-approve.
type knownHostSet struct {
	dests []knownDest
}

// match returns true if host matches a known destination (any path, any method).
func (k *knownHostSet) match(host string) bool {
	return k.matchRequest(host, "", "")
}

// matchRequest returns true if host + URL path + method matches a known destination.
// Empty urlPath or method means "any".
func (k *knownHostSet) matchRequest(host, urlPath, method string) bool {
	for _, d := range k.dests {
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

// add adds an exact host to the set.
func (k *knownHostSet) add(host string) {
	k.dests = append(k.dests, knownDest{Host: host})
}

// readOnly is the default method set: GET and HEAD only.
var readOnly = map[string]bool{"GET": true, "HEAD": true}

// defaultKnownDests are used when no known-hosts.yaml exists.
var defaultKnownDests = []knownDest{
	{Host: "github.com", Methods: readOnly},
	{Host: "api.github.com", Methods: readOnly},
	{Host: "raw.githubusercontent.com", Methods: readOnly},
	{Host: "proxy.golang.org", Methods: readOnly},
	{Host: "sum.golang.org", Methods: readOnly},
	{Host: "registry.npmjs.org", Methods: readOnly},
	{Host: "pypi.org", Methods: readOnly},
	{Host: "files.pythonhosted.org", Methods: readOnly},
	{Host: "rubygems.org", Methods: readOnly},
	{Host: "crates.io", Methods: readOnly},
	{Host: "static.crates.io", Methods: readOnly},
}

// knownHostsPaths returns candidate paths for known-hosts.yaml.
func knownHostsPaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "slagent", "known-hosts.yaml"))
	}
	return paths
}

// loadKnownHosts loads the known host set from ~/.config/slagent/known-hosts.yaml,
// falling back to built-in defaults if the file doesn't exist.
func loadKnownHosts() *knownHostSet {
	set := &knownHostSet{}
	for _, p := range knownHostsPaths() {
		if dests, err := parseKnownHostsFile(p); err == nil {
			set.dests = dests
			return set
		}
	}
	set.dests = append(set.dests, defaultKnownDests...)
	return set
}

// parseKnownHostsFile reads a known-hosts.yaml file.
func parseKnownHostsFile(filePath string) ([]knownDest, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var dests []knownDest
	var current *knownDest
	scanner := bufio.NewScanner(f)
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
			value := unquote(strings.TrimSpace(strings.TrimPrefix(line, "- host:")))
			if value != "" {
				current = &knownDest{Host: value}
			} else {
				current = nil
			}
			continue
		}

		if strings.HasPrefix(line, "path:") && current != nil {
			current.Path = unquote(strings.TrimSpace(strings.TrimPrefix(line, "path:")))
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

// unquote strips surrounding single or double quotes from a YAML value.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
