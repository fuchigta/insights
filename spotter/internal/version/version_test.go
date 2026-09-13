package version_test

import (
	"testing"

	"github.com/fuchigta/spotter/internal/version"
)

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"1.2.3", "v1.2.3", 0},
		{"v1.2.4", "v1.2.3", 1},
		{"v1.2.2", "v1.2.3", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.10.0", "v1.9.0", 1},
		{"v1.2.3-rc1", "v1.2.3", 0},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			got, err := version.Compare(tt.a, tt.b)
			if err != nil {
				t.Fatalf("Compare() error: %v", err)
			}
			sign := func(n int) int {
				switch {
				case n < 0:
					return -1
				case n > 0:
					return 1
				default:
					return 0
				}
			}
			if sign(got) != tt.want {
				t.Errorf("Compare(%q, %q) sign = %d, want %d", tt.a, tt.b, sign(got), tt.want)
			}
		})
	}
}

func TestCompareInvalid(t *testing.T) {
	if _, err := version.Compare("not-a-version", "v1.0.0"); err == nil {
		t.Fatal("不正なバージョン文字列でエラーになりませんでした")
	}
}

func TestSatisfies(t *testing.T) {
	tests := []struct {
		name     string
		current  string
		required string
		want     bool
	}{
		{"required が空なら常に満たす", "v1.0.0", "", true},
		{"同じバージョンは満たす", "v1.2.3", "v1.2.3", true},
		{"上回っていれば満たす", "v1.3.0", "v1.2.3", true},
		{"下回っていれば満たさない", "v1.2.0", "v1.2.3", false},
		{"dev ビルドは判定不能として満たすとみなす", version.Dev, "v1.2.3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := version.Satisfies(tt.current, tt.required)
			if err != nil {
				t.Fatalf("Satisfies() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Satisfies(%q, %q) = %v, want %v", tt.current, tt.required, got, tt.want)
			}
		})
	}
}

func TestSatisfiesInvalidRequired(t *testing.T) {
	if _, err := version.Satisfies("v1.0.0", "not-a-version"); err == nil {
		t.Fatal("required_version が不正な形式なのにエラーになりませんでした")
	}
}
