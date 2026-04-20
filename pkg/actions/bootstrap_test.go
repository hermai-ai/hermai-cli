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
			name: "typical brave desktop file",
			content: `[Desktop Entry]
Name=Brave
Exec=/usr/bin/brave-browser-stable %U
Type=Application`,
			want: "/usr/bin/brave-browser-stable",
		},
		{
			name: "quoted path with spaces",
			content: `[Desktop Entry]
Exec="/opt/Google Chrome/chrome" %U`,
			want: "/opt/Google Chrome/chrome",
		},
		{
			name: "field codes get stripped",
			content: `[Desktop Entry]
Exec=chromium %F --new-window`,
			want: "chromium",
		},
		{
			name: "exec outside [Desktop Entry] section is ignored",
			content: `[Desktop Action NewWindow]
Exec=/usr/bin/IGNORE-ME %u
[Desktop Entry]
Exec=/usr/bin/brave %U`,
			want: "/usr/bin/brave",
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

func TestIsChromiumBinaryName(t *testing.T) {
	cases := map[string]bool{
		"/usr/bin/brave-browser":                    true,
		"/usr/bin/google-chrome-stable":             true,
		"/opt/google/chrome/chrome":                 true,
		"/snap/bin/chromium":                        true,
		"/usr/bin/microsoft-edge":                   true,
		"C:\\Program Files\\Vivaldi\\vivaldi.exe":   true,
		"/Applications/Arc.app/Contents/MacOS/Arc":  true,
		"/usr/bin/firefox":                          false,
		"/usr/bin/librewolf":                        false,
		"/Applications/Safari.app/Contents/MacOS/Safari": false,
		"":                                          false,
	}
	for path, want := range cases {
		if got := isChromiumBinaryName(path); got != want {
			t.Errorf("isChromiumBinaryName(%q) = %v, want %v", path, got, want)
		}
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
