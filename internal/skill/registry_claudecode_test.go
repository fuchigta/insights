// このファイルは package skill_test（外部テストパッケージ）にすることで、
// internal/skill と internal/skill/claudecode の両方を安全に import できる。
// package skill 自身（内部テストを含む）から claudecode を import すると
// import cycle になる（registry.go 冒頭のコメント参照）が、外部テストパッケージは
// どちらからも import されない独立した葉パッケージなので問題にならない。
//
// claudecode パッケージを import した時点で、その init() が
// skill.Register(claudecode.New()) を実行し、レジストリに "claude-code" が
// 登録される。これにより Installers()/ByAgent() の自己登録の仕組みが実際に
// 機能することを検証する。
package skill_test

import (
	"testing"

	"github.com/fuchigta/insights/internal/skill"
	_ "github.com/fuchigta/insights/internal/skill/claudecode"
)

func TestClaudeCodeSelfRegisters(t *testing.T) {
	ins, err := skill.ByAgent("claude-code")
	if err != nil {
		t.Fatalf("ByAgent(\"claude-code\") error = %v", err)
	}
	if ins.Agent() != "claude-code" {
		t.Errorf("Agent() = %q, want %q", ins.Agent(), "claude-code")
	}

	found := false
	for _, i := range skill.Installers() {
		if i.Agent() == "claude-code" {
			found = true
			break
		}
	}
	if !found {
		t.Error("skill.Installers() に claude-code が含まれていません")
	}
}
