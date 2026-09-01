package claudecode

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuchigta/insights/internal/skill"
	"github.com/fuchigta/insights/internal/skill/assets"
)

// newTestInstaller は t.TempDir() を HomeDir/WorkDir として使う Installer を返す。
// 環境変数（HOME 等）は一切書き換えない。
func newTestInstaller(t *testing.T) (*Installer, string, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll(home): %v", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("MkdirAll(work): %v", err)
	}
	return &Installer{HomeDir: home, WorkDir: work}, home, work
}

func TestAgent(t *testing.T) {
	i := New()
	if got := i.Agent(); got != "claude-code" {
		t.Errorf("Agent() = %q, want %q", got, "claude-code")
	}
}

func TestTarget(t *testing.T) {
	i, home, work := newTestInstaller(t)

	gotUser, err := i.Target(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Target(ScopeUser) error = %v", err)
	}
	wantUser := filepath.Join(home, ".claude", "skills", "insights")
	if gotUser != wantUser {
		t.Errorf("Target(ScopeUser) = %q, want %q", gotUser, wantUser)
	}

	gotProject, err := i.Target(skill.ScopeProject)
	if err != nil {
		t.Fatalf("Target(ScopeProject) error = %v", err)
	}
	wantProject := filepath.Join(work, ".claude", "skills", "insights")
	if gotProject != wantProject {
		t.Errorf("Target(ScopeProject) = %q, want %q", gotProject, wantProject)
	}

	if _, err := i.Target(skill.Scope("bogus")); err == nil {
		t.Error("Target(bogus scope) error = nil, want error")
	}
}

func TestDetect(t *testing.T) {
	i, home, _ := newTestInstaller(t)
	i.LookPath = func(string) (string, error) { return "", os.ErrNotExist }

	if i.Detect() {
		t.Error("Detect() = true, want false (no claude in PATH, no ~/.claude)")
	}

	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if !i.Detect() {
		t.Error("Detect() = false, want true (~/.claude exists)")
	}

	i2, _, _ := newTestInstaller(t)
	i2.LookPath = func(file string) (string, error) {
		if file == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", os.ErrNotExist
	}
	if !i2.Detect() {
		t.Error("Detect() = false, want true (claude in PATH)")
	}
}

func TestStatusAbsent(t *testing.T) {
	i, _, _ := newTestInstaller(t)

	st, err := i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.State != skill.StateAbsent {
		t.Errorf("State = %q, want %q", st.State, skill.StateAbsent)
	}
	if st.BundledVersion != assets.Version {
		t.Errorf("BundledVersion = %q, want %q", st.BundledVersion, assets.Version)
	}
}

func TestInstallThenStatusCurrent(t *testing.T) {
	i, _, _ := newTestInstaller(t)

	result, err := i.Install(skill.ScopeUser, false)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if result.From != skill.StateAbsent {
		t.Errorf("Result.From = %q, want %q", result.From, skill.StateAbsent)
	}
	if len(result.Written) != 1 {
		t.Fatalf("len(Result.Written) = %d, want 1", len(result.Written))
	}

	dest := filepath.Join(result.Path, skill.SkillFileName)
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", dest, err)
	}
	if !bytes.Equal(data, assets.SkillMD()) {
		t.Error("書き込まれた SKILL.md が埋め込み内容と一致しません")
	}

	st, err := i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.State != skill.StateCurrent {
		t.Errorf("State = %q, want %q", st.State, skill.StateCurrent)
	}
	if st.InstalledVersion != assets.Version {
		t.Errorf("InstalledVersion = %q, want %q", st.InstalledVersion, assets.Version)
	}
}

func TestStatusModifiedAndForce(t *testing.T) {
	i, _, _ := newTestInstaller(t)

	if _, err := i.Install(skill.ScopeUser, false); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	target, err := i.Target(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	dest := filepath.Join(target, skill.SkillFileName)

	original, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	edited := strings.Replace(string(original), "insights", "insights (手で編集)", 1)
	if err := os.WriteFile(dest, []byte(edited), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	st, err := i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.State != skill.StateModified {
		t.Errorf("State = %q, want %q", st.State, skill.StateModified)
	}

	if _, err := i.Install(skill.ScopeUser, false); err == nil {
		t.Error("Install(force=false) error = nil, want error (StateModified のため)")
	}

	result, err := i.Install(skill.ScopeUser, true)
	if err != nil {
		t.Fatalf("Install(force=true) error = %v", err)
	}
	if result.From != skill.StateModified {
		t.Errorf("Result.From = %q, want %q", result.From, skill.StateModified)
	}

	st2, err := i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st2.State != skill.StateCurrent {
		t.Errorf("State = %q, want %q (force 上書き後)", st2.State, skill.StateCurrent)
	}
}

func TestStatusOutdated(t *testing.T) {
	i, _, _ := newTestInstaller(t)

	if _, err := i.Install(skill.ScopeUser, false); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	target, err := i.Target(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	dest := filepath.Join(target, skill.SkillFileName)

	original, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	old := strings.Replace(
		string(original),
		"insights-version: \""+assets.Version+"\"",
		"insights-version: \"0\"",
		1,
	)
	if old == string(original) {
		t.Fatal("metadata.insights-version の置換に失敗しました（SKILL.md のフォーマットを確認してください）")
	}
	if err := os.WriteFile(dest, []byte(old), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	st, err := i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.State != skill.StateOutdated {
		t.Errorf("State = %q, want %q", st.State, skill.StateOutdated)
	}
	if st.InstalledVersion != "0" {
		t.Errorf("InstalledVersion = %q, want %q", st.InstalledVersion, "0")
	}
}

func TestUninstall(t *testing.T) {
	i, _, _ := newTestInstaller(t)

	// 未導入での Uninstall はエラーにならない。
	if err := i.Uninstall(skill.ScopeUser); err != nil {
		t.Fatalf("Uninstall(未導入) error = %v", err)
	}

	if _, err := i.Install(skill.ScopeUser, false); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	if err := i.Uninstall(skill.ScopeUser); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	st, err := i.Status(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if st.State != skill.StateAbsent {
		t.Errorf("State = %q, want %q (Uninstall 後)", st.State, skill.StateAbsent)
	}

	target, err := i.Target(skill.ScopeUser)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, skill.SkillFileName)); !os.IsNotExist(err) {
		t.Errorf("SKILL.md が残っています: err = %v", err)
	}

	// 2 回目の Uninstall も成功する。
	if err := i.Uninstall(skill.ScopeUser); err != nil {
		t.Fatalf("Uninstall(2 回目) error = %v", err)
	}
}
