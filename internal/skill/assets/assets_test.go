package assets

import (
	"crypto/sha256"
	"encoding/hex"
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

// skillMDFingerprint は SKILL.md 全体（frontmatter を含む）から求めた指紋。
//
// TestSkillMDFrontMatter は frontmatter の insights-version と Version の一致しか見ておらず、
// 本文だけを書き換えても検出できない。internal/judge/prompts の
// TestPromptVersionIsBumpedWhenContentChanges と同じ考え方で、SKILL.md の内容が変わったら
// このテストが落ちるようにし、Version と frontmatter の metadata.insights-version を
// 上げてから下の値を更新することを促す。
const skillMDFingerprint = "2360503216da799573ad78256c3f54696eb9be26645fc7eddf95b428a97a3b07"

func TestSkillMDVersionIsBumpedWhenContentChanges(t *testing.T) {
	h := sha256.New()
	h.Write(SkillMD())
	got := hex.EncodeToString(h.Sum(nil))

	if skillMDFingerprint == "" {
		t.Fatalf("skillMDFingerprint が未設定です。次の値を設定してください: %s (Version=%s)", got, Version)
	}
	if got != skillMDFingerprint {
		t.Errorf(`SKILL.md の内容が変更されています。
  Version と frontmatter の metadata.insights-version を上げてから skillMDFingerprint を更新してください。
  現在の Version: %s
  新しい指紋:     %s`, Version, got)
	}
}
