package actions

import (
	"os"
	"testing"
)

func TestAkamaiValidatedPattern(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		expected bool
	}{
		{
			name:     "validated hit-count -1",
			value:    "0~123~0~-1~abc~2|def|ghi",
			expected: true,
		},
		{
			name:     "still collecting (hit-count 0)",
			value:    "0~123~0~0~abc~2|def|ghi",
			expected: false,
		},
		{
			name:     "still collecting (hit-count 7)",
			value:    "0~123~0~7~abc~2|def|ghi",
			expected: false,
		},
		{
			name:     "live united.com example (unvalidated)",
			value:    "C2C1D6BE5A9D8E10E2F5F6A7B8C9D0E1~0~YAAQxxxxxxxxxxxxx~-1~-1-0-0||dTIjSFExF...",
			expected: true,
		},
		{
			name:     "empty string",
			value:    "",
			expected: false,
		},
		{
			name:     "bare marker without neighbors",
			value:    "~-1~",
			expected: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := akamaiValidatedPattern.MatchString(tc.value)
			if got != tc.expected {
				t.Fatalf("akamaiValidatedPattern.MatchString(%q) = %v, want %v", tc.value, got, tc.expected)
			}
		})
	}
}

func TestParseDesktopExec(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "typical chrome desktop file",
			content: `[Desktop Entry]
Name=Google Chrome
Exec=/usr/bin/google-chrome-stable %U
Type=Application`,
			want: "/usr/bin/google-chrome-stable",
		},
		{
			name: "field codes stripped but args preserved",
			content: `[Desktop Entry]
Exec=chromium %F --new-window`,
			want: "chromium --new-window",
		},
		{
			name: "flatpak wrapper full argv preserved",
			content: `[Desktop Entry]
Exec=/usr/bin/flatpak run --branch=stable --arch=x86_64 --command=chrome com.google.Chrome %U`,
			want: "/usr/bin/flatpak run --branch=stable --arch=x86_64 --command=chrome com.google.Chrome",
		},
		{
			name: "snap wrapper argv preserved",
			content: `[Desktop Entry]
Exec=snap run brave %U`,
			want: "snap run brave",
		},
		{
			name: "exec outside [Desktop Entry] section is ignored",
			content: `[Desktop Action NewWindow]
Exec=/usr/bin/IGNORE-ME %u
[Desktop Entry]
Exec=/usr/bin/google-chrome %U`,
			want: "/usr/bin/google-chrome",
		},
		{
			name: "exec before [Desktop Entry] section is ignored",
			content: `[Preamble]
Exec=/usr/bin/NOT-THIS
[Desktop Entry]
Exec=/usr/bin/YES-THIS`,
			want: "/usr/bin/YES-THIS",
		},
		{
			name:    "empty content",
			content: "",
			want:    "",
		},
		{
			name: "no exec line",
			content: `[Desktop Entry]
Name=Browser`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDesktopExec(tc.content)
			if got != tc.want {
				t.Errorf("parseDesktopExec() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFirstExecToken(t *testing.T) {
	cases := map[string]string{
		"/usr/bin/google-chrome --new-window": "/usr/bin/google-chrome",
		`"/opt/Google Chrome/chrome" %U`:      "/opt/Google Chrome/chrome",
		"":                                    "",
		"   ":                                 "",
		`"unterminated`:                       "",
	}
	for in, want := range cases {
		if got := firstExecToken(in); got != want {
			t.Errorf("firstExecToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractFlatpakBundleID(t *testing.T) {
	cases := map[string]string{
		// Real flatpak browser Exec= lines
		"/usr/bin/flatpak run --branch=stable --arch=x86_64 --command=chrome com.google.Chrome": "com.google.Chrome",
		"/usr/bin/flatpak run com.brave.Browser":                                                 "com.brave.Browser",
		"/usr/bin/flatpak run org.chromium.Chromium @@u @@":                                      "org.chromium.Chromium",
		// No bundle-ID-shaped token anywhere in the line
		"/usr/bin/flatpak run --version":                                           "",
		"/usr/bin/google-chrome --proxy-server=127.0.0.1:8080":                     "",
		"":                                                                        "",
		// Tokens that look bundle-id-adjacent but must be rejected: too few dots,
		// path separators, empty labels, characters outside the allowed class.
		"/usr/bin/flatpak run com.foo":                "", // only one dot → rejected
		"/usr/bin/flatpak run com/google/chrome":     "", // path separators → rejected
		"/usr/bin/flatpak run com..google.chrome":    "", // empty dot-separated label → rejected
		"/usr/bin/flatpak run .com.google.chrome":    "", // leading dot → rejected
		"/usr/bin/flatpak run com.google.chrome.":    "", // trailing dot → rejected
		"/usr/bin/flatpak run --branch=stable":        "", // = sign → rejected
	}
	for in, want := range cases {
		if got := extractFlatpakBundleID(in); got != want {
			t.Errorf("extractFlatpakBundleID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractSnapName(t *testing.T) {
	cases := map[string]string{
		"snap run brave":                "brave",
		"snap run --shell chromium":     "chromium",
		"/snap/bin/brave":               "",
		"snap run":                      "",
		"":                              "",
		"google-chrome --new-window":    "",
	}
	for in, want := range cases {
		if got := extractSnapName(in); got != want {
			t.Errorf("extractSnapName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRegSZValue(t *testing.T) {
	progIDOutput := `
HKEY_CURRENT_USER\SOFTWARE\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice
    ProgId    REG_SZ    ChromeHTML
    Hash      REG_SZ    xxxxxxxxxxxxxxxxxxxx=
`
	if got := parseRegSZValue(progIDOutput, "ProgId"); got != "ChromeHTML" {
		t.Errorf("ProgId lookup got %q, want ChromeHTML", got)
	}

	shellOpenOutput := `
HKEY_CLASSES_ROOT\ChromeHTML\shell\open\command
    (Default)    REG_SZ    "C:\Program Files\Google\Chrome\Application\chrome.exe" --single-argument %1
`
	want := `"C:\Program Files\Google\Chrome\Application\chrome.exe" --single-argument %1`
	if got := parseRegSZValue(shellOpenOutput, "(Default)"); got != want {
		t.Errorf("(Default) lookup got %q, want %q", got, want)
	}

	// Asking for a value that doesn't exist returns empty.
	if got := parseRegSZValue(progIDOutput, "DoesNotExist"); got != "" {
		t.Errorf("missing value should return \"\", got %q", got)
	}

	// Header lines containing REG_SZ in unexpected places must not spoof the parse.
	spoof := `The REG_SZ format is described in the reg.exe docs.
    (Default)    REG_SZ    C:\real\value.exe
`
	if got := parseRegSZValue(spoof, "(Default)"); got != `C:\real\value.exe` {
		t.Errorf("spoof-resistant lookup got %q", got)
	}
}

func TestIsChromiumBinaryName(t *testing.T) {
	cases := map[string]bool{
		// Positive: known Chromium-family binary names
		"/usr/bin/google-chrome":                         true,
		"/usr/bin/google-chrome-stable":                  true,
		"/opt/google/chrome/chrome":                      true,
		"/snap/bin/chromium":                             true,
		"/usr/bin/chromium-browser":                      true,
		"/usr/bin/microsoft-edge":                        true,
		"/usr/bin/microsoft-edge-stable":                 true,
		"/usr/bin/brave-browser":                         true,
		"/usr/bin/brave-browser-nightly":                 true,
		// Bare Windows .exe basenames (filepath.Base doesn't handle Windows
		// backslashes on non-Windows hosts, so we assert the basename form
		// here and rely on runtime.GOOS="windows" doing the right thing).
		"chrome.exe":  true,
		"msedge.exe":  true,
		"vivaldi.exe": true,
		"brave.exe":   true,
		"/Applications/Arc.app/Contents/MacOS/Arc":      true,
		// Negative: non-Chromium browsers
		"/usr/bin/firefox":                               false,
		"/usr/bin/librewolf":                             false,
		"/Applications/Safari.app/Contents/MacOS/Safari": false,
		// Negative: words that would have matched substring-only ("arc", "edge", "opera", "chrome")
		"/usr/bin/research":                              false,
		"/usr/bin/hedgehog":                              false,
		"/usr/bin/wedge":                                 false,
		"/usr/bin/ledger":                                false,
		"/usr/bin/helichrome":                            false,
		"/usr/bin/operations":                            false,
		"":                                               false,
	}
	for path, want := range cases {
		if got := isChromiumBinaryName(path); got != want {
			t.Errorf("isChromiumBinaryName(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestFindSystemChromiumBinarySmoke(t *testing.T) {
	// Contract smoke: findSystemChromiumBinary must either return a path
	// that currently exists or an empty string — never a stale path or
	// an un-runnable command. Runs on whatever OS + browser mix the dev
	// machine has, so specific assertions aren't portable.
	bin := findSystemChromiumBinary()
	t.Logf("findSystemChromiumBinary = %q", bin)
	if bin == "" {
		t.Skip("no Chromium-family browser found on this host — skipping contract assertion")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Errorf("findSystemChromiumBinary returned %q which does not exist on disk: %v", bin, err)
	}
	if !isChromiumBinaryName(bin) {
		t.Errorf("findSystemChromiumBinary returned %q which our own allow-list rejects", bin)
	}
}

func TestExtractExeFromShellOpenCommand(t *testing.T) {
	cases := map[string]string{
		`"C:\Program Files\Google\Chrome\Application\chrome.exe" --single-argument %1`: `C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`"C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe" -- "%1"`: `C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
		`C:\Program\ Files\Chromium\chrome.exe %1`:                                     `C:\Program\`, // unquoted path with escaped space — we take the first token
		`chrome.exe %1`:                                                                `chrome.exe`,
		``:                                                                             "",
		`   `:                                                                          "",
		`"unterminated`:                                                                 "",
	}
	for in, want := range cases {
		if got := extractExeFromShellOpenCommand(in); got != want {
			t.Errorf("extractExeFromShellOpenCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsChromiumBundleID(t *testing.T) {
	if !isChromiumBundleID("com.google.chrome") || !isChromiumBundleID("com.brave.browser") {
		t.Errorf("canonical Chromium-family IDs should match")
	}
	if isChromiumBundleID("com.apple.safari") || isChromiumBundleID("org.mozilla.firefox") {
		t.Errorf("non-Chromium IDs should not match")
	}
	if isChromiumBundleID("") {
		t.Errorf("empty ID should not match")
	}
}

func TestExpandWindowsEnvVars(t *testing.T) {
	os.Setenv("HERMAI_TEST_LOCALAPP", `C:\Users\tester\AppData\Local`)
	defer os.Unsetenv("HERMAI_TEST_LOCALAPP")

	cases := map[string]string{
		`%HERMAI_TEST_LOCALAPP%\Google\Chrome\chrome.exe`: `C:\Users\tester\AppData\Local\Google\Chrome\chrome.exe`,
		`C:\Program Files\chrome.exe`:                     `C:\Program Files\chrome.exe`,
		`%HERMAI_DOES_NOT_EXIST%\chrome.exe`:              `%HERMAI_DOES_NOT_EXIST%\chrome.exe`,
		`%HERMAI_TEST_LOCALAPP%\%HERMAI_TEST_LOCALAPP%`: `C:\Users\tester\AppData\Local\C:\Users\tester\AppData\Local`,
	}
	for in, want := range cases {
		if got := expandWindowsEnvVars(in); got != want {
			t.Errorf("expandWindowsEnvVars(%q) = %q, want %q", in, got, want)
		}
	}
}
