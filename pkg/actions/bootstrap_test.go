package actions

import (
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
