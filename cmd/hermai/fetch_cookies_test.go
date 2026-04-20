package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hermai-ai/hermai-cli/pkg/actions"
)

// writeJar saves a CookieFile under storageDir/site/cookies.json so
// mergeCookiesForURL can find it via LoadCookieFile.
func writeJar(t *testing.T, storageDir, site string, cookies map[string]string) {
	t.Helper()
	dir := filepath.Join(storageDir, site)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := actions.CookieFile{Site: site, Domain: site, Cookies: cookies}
	body, _ := json.Marshal(file)
	if err := os.WriteFile(filepath.Join(dir, "cookies.json"), body, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// sortedKeys normalizes a `name=value` cookie slice to just sorted names so
// tests assert on content without caring about map iteration order.
func sortedKeys(pairs []string) []string {
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if name, _, ok := strings.Cut(p, "="); ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func TestMergeCookiesForURL_ExactHostMatch(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, dir, "example.com", map[string]string{"session": "abc", "locale": "en"})

	got := mergeCookiesForURL(dir, "https://example.com/path", nil, nil)
	want := []string{"locale", "session"}
	if diff := sortedKeys(got); !equalSlices(diff, want) {
		t.Fatalf("cookie names = %v, want %v", diff, want)
	}
}

func TestMergeCookiesForURL_LabelStripping(t *testing.T) {
	// Jar under "example.com" should serve requests to "www.example.com",
	// "api.example.com", and "api.www.example.com" — matches how browsers
	// scope cookies to the registrable domain.
	dir := t.TempDir()
	writeJar(t, dir, "example.com", map[string]string{"session": "abc"})

	for _, host := range []string{"www.example.com", "api.example.com", "api.www.example.com"} {
		got := mergeCookiesForURL(dir, "https://"+host+"/x", nil, nil)
		if names := sortedKeys(got); !equalSlices(names, []string{"session"}) {
			t.Errorf("host %s: got %v, want [session]", host, names)
		}
	}
}

func TestMergeCookiesForURL_MissingJarReturnsInput(t *testing.T) {
	// No jar on disk. Caller's explicit --cookie values must be preserved
	// unchanged — a missing jar is not an error path.
	dir := t.TempDir()
	explicit := []string{"foo=bar", "baz=qux"}
	got := mergeCookiesForURL(dir, "https://example.com/", explicit, nil)
	if names := sortedKeys(got); !equalSlices(names, []string{"baz", "foo"}) {
		t.Fatalf("got %v, want [baz foo]", names)
	}
}

func TestMergeCookiesForURL_ExplicitOverridesJar(t *testing.T) {
	// Jar carries session=oldvalue; caller passes --cookie session=newvalue.
	// The explicit value wins, and previously-absent cookies from the caller
	// are appended alongside the jar's other entries.
	dir := t.TempDir()
	writeJar(t, dir, "example.com", map[string]string{"session": "oldvalue", "locale": "en"})
	got := mergeCookiesForURL(dir, "https://example.com/", []string{"session=newvalue", "ab_test=1"}, nil)

	asMap := make(map[string]string)
	for _, p := range got {
		if n, v, ok := strings.Cut(p, "="); ok {
			asMap[n] = v
		}
	}
	if asMap["session"] != "newvalue" {
		t.Errorf("session should have been overridden: %q", asMap["session"])
	}
	if asMap["locale"] != "en" {
		t.Errorf("locale from jar should survive: %q", asMap["locale"])
	}
	if asMap["ab_test"] != "1" {
		t.Errorf("ab_test from caller should be appended: %q", asMap["ab_test"])
	}
}

func TestMergeCookiesForURL_BadURLReturnsInput(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, dir, "example.com", map[string]string{"session": "abc"})

	got := mergeCookiesForURL(dir, "::::not a url::::", []string{"foo=bar"}, nil)
	if names := sortedKeys(got); !equalSlices(names, []string{"foo"}) {
		t.Fatalf("got %v, want [foo]", names)
	}
}

func TestMergeCookiesForURL_StopsAtTLD(t *testing.T) {
	// A jar saved as "com" must never match "example.com" — the fallback
	// walk stops one label short of the TLD to avoid cross-site leakage.
	dir := t.TempDir()
	writeJar(t, dir, "com", map[string]string{"session": "leaked"})

	got := mergeCookiesForURL(dir, "https://example.com/", nil, nil)
	if len(got) != 0 {
		t.Fatalf("unexpected cookies attached: %v", got)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
