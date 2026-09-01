package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuchigta/insights/internal/skill"
	"github.com/fuchigta/insights/internal/skill/assets"
)

// newTestInstaller は t.TempDir() を CodexHome/WorkDir として使う Installer を返す。
// 環境変数（CODEX_HOME 等）は書き換えない。
func newTestInstaller(t *testing.T) (*Installer, string, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "codex-home")
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll(home): %v", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("MkdirAll(work): %v", err)
	}
	return &Installer{CodexHome: home, WorkDir: work}, home, work
}

func TestAgent(t *testing.T) {
	if got := New().Agent(); got != agentName {
		t.Errorf("Agent() = %q, want %q", got, agentName)
	}
}

// TestRegisteredInRegistry は init() での自己登録が効いていることを確かめる。
// 登録されていないと `insights skill install --agent codex` が
// 「未知のエージェント」で落ちる。
func TestRegisteredInRegistry(t *testing.T) {
	got, err := skill.ByAgent(agentName)
	if err != nil {
		t.Fatalf("ByAgent(%q) error = %v", agentName, err)
	}
	if got.Agent() != agentName {
		t.Errorf("ByAgent(%q).Agent() = %q", agentName, got.Agent())
	}
}

func TestTarget(t *testing.T) {
	i, home, work := newTestInstaller(t)

	gotUser, err := i.Target(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Target(ScopeUser) error = %v", err)
	}
	if want := filepath.Join(home, "skills", "insights"); gotUser != want {
		t.Errorf("Target(ScopeUser) = %q, want %q", gotUser, want)
	}

	gotProject, err := i.Target(skill.ScopeProject)
	if err != nil {
		t.Fatalf("Target(ScopeProject) error = %v", err)
	}
	if want := filepath.Join(work, ".codex", "skills", "insights"); gotProject != want {
		t.Errorf("Target(ScopeProject) = %q, want %q", gotProject, want)
	}

	if _, err := i.Target(skill.Scope("bogus")); err == nil {
		t.Error("Target(bogus scope) error = nil, want error")
	}
}

// TestTarget_HonorsCodexHomeEnv は CODEX_HOME を見ることを確かめる。
// Codex 自身がこの環境変数でホームを移すため、無視すると Codex が読まない場所に
// 置いてしまい、導入したのに使えないという最悪の失敗になる。
func TestTarget_HonorsCodexHomeEnv(t *testing.T) {
	moved := t.TempDir()
	t.Setenv("CODEX_HOME", moved)

	got, err := (&Installer{}).Target(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Target(ScopeUser) error = %v", err)
	}
	if want := filepath.Join(moved, "skills", "insights"); got != want {
		t.Errorf("Target(ScopeUser) = %q, want %q", got, want)
	}
}

func TestDetect(t *testing.T) {
	i, home, _ := newTestInstaller(t)
	i.LookPath = func(string) (string, error) { return "", os.ErrNotExist }

	// newTestInstaller は CodexHome を作ってしまうので、まず消して「無い」状態にする。
	if err := os.RemoveAll(home); err != nil {
		t.Fatalf("RemoveAll(home): %v", err)
	}
	if i.Detect() {
		t.Error("Detect() = true, want false（PATH にも CODEX_HOME にも無い）")
	}

	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll(home): %v", err)
	}
	if !i.Detect() {
		t.Error("Detect() = false, want true（CODEX_HOME が存在する）")
	}

	i2, home2, _ := newTestInstaller(t)
	if err := os.RemoveAll(home2); err != nil {
		t.Fatalf("RemoveAll(home2): %v", err)
	}
	i2.LookPath = func(file string) (string, error) {
		if file == "codex" {
			return "/usr/bin/codex", nil
		}
		return "", os.ErrNotExist
	}
	if !i2.Detect() {
		t.Error("Detect() = false, want true（codex が PATH にある）")
	}
}

func TestInstallStatusUninstall(t *testing.T) {
	i, _, _ := newTestInstaller(t)

	st, err := i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.State != skill.StateAbsent {
		t.Fatalf("State = %q, want %q", st.State, skill.StateAbsent)
	}
	if st.Agent != agentName {
		t.Errorf("Status.Agent = %q, want %q", st.Agent, agentName)
	}

	result, err := i.Install(skill.ScopeUser, false)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	dest := filepath.Join(result.Path, skill.SkillFileName)
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", dest, err)
	}
	if !bytes.Equal(data, assets.SkillMD()) {
		t.Error("書き込まれた SKILL.md が埋め込み内容と一致しません")
	}

	st, err = i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.State != skill.StateCurrent {
		t.Errorf("State = %q, want %q", st.State, skill.StateCurrent)
	}

	// 手で書き換えたら force 無しでは上書きしない。
	if err := os.WriteFile(dest, []byte("---\nname: insights\nmetadata:\n  insights-version: \""+assets.Version+"\"\n---\n書き換えた\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	st, err = i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.State != skill.StateModified {
		t.Fatalf("State = %q, want %q", st.State, skill.StateModified)
	}
	if _, err := i.Install(skill.ScopeUser, false); err == nil {
		t.Error("Install(force=false) = nil, want error（改変済み）")
	}
	if _, err := i.Install(skill.ScopeUser, true); err != nil {
		t.Errorf("Install(force=true) error = %v", err)
	}

	if err := i.Uninstall(skill.ScopeUser); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("Uninstall 後も SKILL.md が残っています: %v", err)
	}
	// 未導入に対する Uninstall は成功する（冪等）。
	if err := i.Uninstall(skill.ScopeUser); err != nil {
		t.Errorf("Uninstall(2 回目) error = %v", err)
	}
}

// TestUninstall_KeepsOtherSkills は、同じ skills/ 配下にある他のスキルを
// 巻き添えで消さないことを確かめる。
func TestUninstall_KeepsOtherSkills(t *testing.T) {
	i, home, _ := newTestInstaller(t)

	if _, err := i.Install(skill.ScopeUser, false); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	other := filepath.Join(home, "skills", "someone-elses")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(other, skill.SkillFileName), []byte("---\n---\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := i.Uninstall(skill.ScopeUser); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, skill.SkillFileName)); err != nil {
		t.Errorf("他のスキルが消えています: %v", err)
	}
}
