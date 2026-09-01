package skill

import (
	"testing"

	"github.com/fuchigta/insights/internal/skill/assets"
)

func TestParseFrontMatter(t *testing.T) {
	fm, err := parseFrontMatter(assets.SkillMD())
	if err != nil {
		t.Fatalf("parseFrontMatter() error = %v", err)
	}
	if fm.Name != "insights" {
		t.Errorf("Name = %q, want %q", fm.Name, "insights")
	}
	if fm.version() != assets.Version {
		t.Errorf("version() = %q, want %q", fm.version(), assets.Version)
	}
}

// TestParseFrontMatter_LegacyVersionKey は、metadata へ移す前に配置された SKILL.md
// （トップレベルの x-insights-version）からもバージョンを読めることを確認する。
// 読めないと、旧バージョンの導入済みスキルが outdated ではなく「改変済み」に見え、
// 再インストールに --force を要求してしまう。
func TestParseFrontMatter_LegacyVersionKey(t *testing.T) {
	legacy := []byte(`---
name: insights
x-insights-version: "1"
---

# insights
`)

	fm, err := parseFrontMatter(legacy)
	if err != nil {
		t.Fatalf("parseFrontMatter() error = %v", err)
	}
	if fm.version() != "1" {
		t.Errorf("version() = %q, want %q", fm.version(), "1")
	}
}
