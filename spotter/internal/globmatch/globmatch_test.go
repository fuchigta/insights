package globmatch

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{"星がスラッシュをまたぐ", "internal/source/*/*.go", "internal/source/claude/parse.go", true},
		{"星は複数階層にもまたがる", "internal/*", "internal/a/b/c.go", true},
		{"完全一致のみ許可", "README.md", "README.md.bak", false},
		{"疑問符は1文字だけ", "a?c", "abc", true},
		{"疑問符は2文字にはマッチしない", "a?c", "abbc", false},
		{"文字クラス", "file[0-9].txt", "file1.txt", true},
		{"文字クラス範囲外", "file[0-9].txt", "filea.txt", false},
		{"否定クラス", "file[!0-9].txt", "filea.txt", true},
		{"否定クラスに数字は不一致", "file[!0-9].txt", "file1.txt", false},
		{"正規表現メタ文字はリテラル扱い", "a.b+c", "a.b+c", true},
		{"正規表現メタ文字はエスケープされる", "a.b+c", "axbbc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Match(tt.pattern, tt.input)
			if err != nil {
				t.Fatalf("Match(%q, %q) error: %v", tt.pattern, tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.input, got, tt.want)
			}
		})
	}
}
