package render

import "testing"

func TestCleanLabel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short passthrough", "go build", 40, "go build"},
		{"empty stays empty", "", 40, ""},
		{
			"multiline command collapses to one line",
			"bash -c \n  grotto mark build\n  go build ./...",
			60,
			"bash -c grotto mark build go build ./...",
		},
		{
			"collapsed line truncates with ellipsis",
			"bash -c \n  grotto mark build\n  CGO_ENABLED=0 go build ./...",
			20,
			"bash -c grotto mark…",
		},
		{"internal tabs and spaces collapse", "go\t\tbuild   ./...", 40, "go build ./..."},
		{"whitespace-only becomes empty", "   \n\t  ", 40, ""},
		{"non-positive max disables truncation", "bash -c \n one two three", 0, "bash -c one two three"},
		{"rune-aware truncation does not split multibyte", "日本語テスト実行", 4, "日本語…"},
		{"exact-length passthrough", "abcd", 4, "abcd"},
		{"max one renders bare ellipsis", "abcdef", 1, "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanLabel(tt.in, tt.max); got != tt.want {
				t.Errorf("CleanLabel(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}
