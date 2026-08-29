package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/skill"
	"github.com/fuchigta/insights/internal/skill/claudecode"
)

// --- テスト用ヘルパ ---

// runSkillCLI は NewRootCommand + newSkillCommand を組み合わせて実行する。
// root.go 自体は変更していないため、この組み立てはテスト側で毎回行う。
// skill コマンドは Config を使わないため、--config には存在しないパスを渡しても
// 問題ない（config.Load はファイルが無ければ既定値を返すだけで、実ホームには
// 一切アクセスしない）。
func runSkillCLI(t *testing.T, configPath, dbPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand("test")
	root.AddCommand(newSkillCommand())

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)

	fullArgs := append([]string{"--config", configPath, "--db", dbPath, "skill"}, args...)
	root.SetArgs(fullArgs)

	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// mustRunSkillJSON は --json を付けて実行し、失敗したら Fatal する。stdout を返す。
func mustRunSkillJSON(t *testing.T, configPath, dbPath string, args ...string) string {
	t.Helper()
	stdout, _, err := runSkillCLI(t, configPath, dbPath, append(append([]string{}, args...), "--json")...)
	if err != nil {
		t.Fatalf("skill %v の実行に失敗しました: %v (stdout=%s)", args, err, stdout)
	}
	return stdout
}

func decodeSkillJSON(t *testing.T, raw string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		t.Fatalf("JSON デコードに失敗しました: %v (raw=%s)", err, raw)
	}
}

// --- テスト本体 ---

// TestSkillInstallStatusUninstallFlow は install -> status -> 手動編集 -> force無しでエラー
// -> force 付きで成功 -> uninstall -> status(absent) -> 2回目のuninstallも成功、の一連を確認する。
//
// internal/skill はエージェント実装（claudecode.Installer）をレジストリへの自己登録方式で
// 解決するため（internal/skill/registry.go 参照）、CLI 層は skill.ByAgent("claude-code") で
// レジストリ登録済みの Installer を引く。既定で登録されるインスタンスは実ホーム
// （os.UserHomeDir）を指してしまうため、このテストでは
// skill.Register(&claudecode.Installer{HomeDir: ..., WorkDir: ...}) で一時ディレクトリを指す
// インスタンスに差し替えてから実行する（Register は同名 Agent() を後勝ちで上書きする設計）。
// これにより実ホームの ~/.claude/skills/ には一切書き込まない。
func TestSkillInstallStatusUninstallFlow(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll(home): %v", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("MkdirAll(work): %v", err)
	}

	skill.Register(&claudecode.Installer{HomeDir: home, WorkDir: work})

	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	// 1. 未導入の status
	stdout := mustRunSkillJSON(t, configPath, dbPath, "status")
	var st skillStatusView
	decodeSkillJSON(t, stdout, &st)
	if st.State != string(skill.StateAbsent) {
		t.Fatalf("初期状態は absent のはず: %+v", st)
	}
	if !strings.HasPrefix(st.Path, home) {
		t.Fatalf("配置先が一時ディレクトリの外を指しています: %s (home=%s)", st.Path, home)
	}

	// 2. install
	stdout = mustRunSkillJSON(t, configPath, dbPath, "install")
	var inst skillInstallResult
	decodeSkillJSON(t, stdout, &inst)
	if inst.PreviousState != string(skill.StateAbsent) {
		t.Errorf("PreviousState = %q, want %q", inst.PreviousState, skill.StateAbsent)
	}
	if len(inst.Written) == 0 {
		t.Errorf("Written が空です")
	}

	// 3. status: current
	stdout = mustRunSkillJSON(t, configPath, dbPath, "status")
	decodeSkillJSON(t, stdout, &st)
	if st.State != string(skill.StateCurrent) {
		t.Errorf("インストール後は current のはず: %+v", st)
	}
	if st.InstalledVersion == "" || st.InstalledVersion != st.BundledVersion {
		t.Errorf("InstalledVersion が BundledVersion と一致しません: %+v", st)
	}

	// 4. 手で書き換えてから --force なしで install -> エラー
	skillFile := filepath.Join(st.Path, "SKILL.md")
	original, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	edited := append(append([]byte{}, original...), []byte("\n<!-- edited -->\n")...)
	if err := os.WriteFile(skillFile, edited, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, err = runSkillCLI(t, configPath, dbPath, "install")
	if err == nil {
		t.Fatal("手で編集後、--force なしの install はエラーになるはず")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("エラーメッセージに --force の案内が含まれていません: %v", err)
	}

	// 5. --force ありなら成功する
	if _, _, err := runSkillCLI(t, configPath, dbPath, "install", "--force"); err != nil {
		t.Fatalf("--force 付き install に失敗しました: %v", err)
	}
	stdout = mustRunSkillJSON(t, configPath, dbPath, "status")
	decodeSkillJSON(t, stdout, &st)
	if st.State != string(skill.StateCurrent) {
		t.Errorf("--force 上書き後は current のはず: %+v", st)
	}

	// 6. uninstall
	stdout = mustRunSkillJSON(t, configPath, dbPath, "uninstall")
	var un skillUninstallResult
	decodeSkillJSON(t, stdout, &un)
	if !un.WasInstalled {
		t.Errorf("WasInstalled = false, want true")
	}

	// 7. status: absent に戻る
	stdout = mustRunSkillJSON(t, configPath, dbPath, "status")
	decodeSkillJSON(t, stdout, &st)
	if st.State != string(skill.StateAbsent) {
		t.Errorf("アンインストール後は absent のはず: %+v", st)
	}
	if _, statErr := os.Stat(skillFile); !os.IsNotExist(statErr) {
		t.Errorf("SKILL.md が削除されていません: err = %v", statErr)
	}

	// 8. 2 回目の uninstall も「導入されていません」で正常終了する
	stdout2, _, err := runSkillCLI(t, configPath, dbPath, "uninstall")
	if err != nil {
		t.Fatalf("未導入の uninstall はエラーにならないはず: %v", err)
	}
	if !strings.Contains(stdout2, "導入されていません") {
		t.Errorf("案内メッセージが含まれていません: %s", stdout2)
	}
}

// TestSkill_ProjectScope は --scope project がユーザースコープと別の配置先になることを確認する。
func TestSkill_ProjectScope(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll(home): %v", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("MkdirAll(work): %v", err)
	}
	skill.Register(&claudecode.Installer{HomeDir: home, WorkDir: work})

	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	stdout := mustRunSkillJSON(t, configPath, dbPath, "status", "--scope", "project")
	var st skillStatusView
	decodeSkillJSON(t, stdout, &st)
	if !strings.HasPrefix(st.Path, work) {
		t.Errorf("--scope project の配置先が WorkDir 配下ではありません: %s (work=%s)", st.Path, work)
	}
}

// TestSkill_UnknownAgentListsAvailable は --agent に未知の名前を渡したとき、
// 利用可能なエージェント名を列挙したエラーになることを確認する。
func TestSkill_UnknownAgentListsAvailable(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	_, _, err := runSkillCLI(t, configPath, dbPath, "status", "--agent", "no-such-agent")
	if err == nil {
		t.Fatal("未知のエージェントはエラーになるはず")
	}
	if !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("利用可能なエージェント名（claude-code）が案内されていません: %v", err)
	}
}

// TestSkill_UnknownScopeIsError は --scope に user/project 以外を渡したときエラーになることを確認する。
func TestSkill_UnknownScopeIsError(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	dbPath := filepath.Join(tmp, "insights.db")

	if _, _, err := runSkillCLI(t, configPath, dbPath, "status", "--scope", "bogus"); err == nil {
		t.Fatal("未知の --scope はエラーになるはず")
	}
}
