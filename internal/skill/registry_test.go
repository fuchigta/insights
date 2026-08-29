package skill

import "testing"

// fakeInstaller は registry.go 単体の挙動（実装パッケージに依存しない範囲）を
// テストするためのダミー実装。internal/skill/claudecode をここから import すると
// import cycle になるため使えない（registry.go 冒頭のコメント参照）。
type fakeInstaller struct {
	agent string
}

func (f *fakeInstaller) Agent() string                { return f.agent }
func (f *fakeInstaller) Detect() bool                 { return true }
func (f *fakeInstaller) Target(Scope) (string, error) { return "", nil }
func (f *fakeInstaller) Install(Scope, bool) (Result, error) {
	return Result{}, nil
}
func (f *fakeInstaller) Status(Scope) (Status, error) { return Status{}, nil }
func (f *fakeInstaller) Uninstall(Scope) error        { return nil }

func TestByAgentUnknown(t *testing.T) {
	if _, err := ByAgent("no-such-agent-xyz"); err == nil {
		t.Error("ByAgent(未知の名前) error = nil, want error")
	}
}

func TestRegisterAndByAgent(t *testing.T) {
	name := "fake-agent-for-test"
	Register(&fakeInstaller{agent: name})

	got, err := ByAgent(name)
	if err != nil {
		t.Fatalf("ByAgent(%q) error = %v", name, err)
	}
	if got.Agent() != name {
		t.Errorf("Agent() = %q, want %q", got.Agent(), name)
	}

	found := false
	for _, ins := range Installers() {
		if ins.Agent() == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Installers() に %q が含まれていません", name)
	}
}

func TestRegisterNilIsNoop(t *testing.T) {
	before := len(Installers())
	Register(nil)
	if got := len(Installers()); got != before {
		t.Errorf("Register(nil) 後の Installers() の件数 = %d, want %d（変化しないはず）", got, before)
	}
}
