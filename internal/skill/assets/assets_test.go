package assets

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// frontMatter は SKILL.md 先頭の YAML frontmatter のうち、このテストで検証する部分だけ。
type frontMatter struct {
	Name     string `yaml:"name"`
	Metadata struct {
		Version string `yaml:"insights-version"`
	} `yaml:"metadata"`
}

func parseFrontMatter(t *testing.T, data []byte) frontMatter {
	t.Helper()

	const delim = "---"
	raw := string(data)
	if !strings.HasPrefix(raw, delim+"\n") {
		t.Fatalf("SKILL.md の先頭が frontmatter 区切り %q ではありません", delim)
	}
	rest := raw[len(delim)+1:]
	end := strings.Index(rest, "\n"+delim)
	if end < 0 {
		t.Fatalf("SKILL.md の frontmatter 終端 %q が見つかりません", delim)
	}

	var fm frontMatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		t.Fatalf("frontmatter の YAML デコードに失敗しました: %v", err)
	}
	return fm
}

func TestSkillMDFrontMatter(t *testing.T) {
	fm := parseFrontMatter(t, SkillMD())

	if fm.Name != "insights" {
		t.Errorf("name = %q, want %q", fm.Name, "insights")
	}
	if fm.Metadata.Version != Version {
		t.Errorf("metadata.insights-version = %q, want %q (assets.Version)", fm.Metadata.Version, Version)
	}
}

func TestSkillMDCopyIsIndependent(t *testing.T) {
	a := SkillMD()
	if len(a) == 0 {
		t.Fatal("SkillMD() が空です")
	}
	a[0] = 'X'

	b := SkillMD()
	if b[0] == 'X' {
		t.Error("SkillMD() が呼び出し側の変更の影響を受けています（コピーを返していない）")
	}
}
