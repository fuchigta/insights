package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/update"
	"github.com/spf13/cobra"
)

// --- テスト用ヘルパ ---

// fakeReleaseServer は「最新は tag である」とだけ答えるサーバを立て、
// updateBaseURL をそこへ向ける。呼ばれた回数を返すので、キャッシュが効いて
// ネットワークに出ていないことも確かめられる。
//
// テストが実際の GitHub に触れないための土台。updateBaseURL は
// internal/cli/deps.go の newJudge と同じく「差し替えのための唯一の穴」。
func fakeReleaseServer(t *testing.T, tag string) (url string, hits *atomic.Int64) {
	t.Helper()

	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			count.Add(1)
			http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	prev := updateBaseURL
	updateBaseURL = srv.URL
	t.Cleanup(func() { updateBaseURL = prev })

	return srv.URL, &count
}

// allowUpdateNotice は更新通知の端末判定を無効化する。
//
// 既定では標準エラーが端末のときにしか通知しない（cron のログを汚さないため）が、
// テストではバッファへ書かせるので端末にならない。通知の中身を検証するテストだけが
// この穴を開ける。
func allowUpdateNotice(t *testing.T) {
	t.Helper()
	prev := updateNoticeAllowed
	updateNoticeAllowed = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { updateNoticeAllowed = prev })
}

// writeUpdateTestConfig は更新確認のテスト用に最小限の設定を書き出す。
// ログソースはどちらも切って、doctor が実行環境のログを読まないようにする。
func writeUpdateTestConfig(t *testing.T, configPath string, mutate func(*config.Config)) {
	t.Helper()
	cfg := config.Default()
	cfg.Sources.ClaudeCode.Enabled = false
	cfg.Sources.Codex.Enabled = false
	isolateCodexSource(t, cfg)
	cfg.Database = filepath.Join(filepath.Dir(configPath), "insights.db")
	cfg.Output.Dir = filepath.Join(filepath.Dir(configPath), "reports")
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}
}

// runCLI は version を埋め込んだルートコマンドを組み立てて実行する。
func runCLI(t *testing.T, version, configPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand(version)

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--config", configPath}, args...))

	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// --- insights update ---

func TestUpdateCheckOnly(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, nil)
	fakeReleaseServer(t, "v9.9.9")

	stdout, _, err := runCLI(t, "v1.0.0", configPath, "update", "--check", "--json")
	if err != nil {
		t.Fatalf("update --check: %v (stdout=%s)", err, stdout)
	}

	var view updateResult
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("JSON デコードに失敗しました: %v (raw=%s)", err, stdout)
	}
	if !view.UpdateAvailable || view.Latest != "v9.9.9" || view.Current != "v1.0.0" {
		t.Errorf("update --check = %+v", view)
	}
	if view.Applied {
		t.Error("--check なのに置き換えている")
	}
	if view.Method != string(update.MethodRelease) {
		t.Errorf("Method = %q, 期待 %q", view.Method, update.MethodRelease)
	}
}

func TestUpdateAlreadyLatest(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, nil)
	fakeReleaseServer(t, "v1.0.0")

	stdout, _, err := runCLI(t, "v1.0.0", configPath, "update", "--json")
	if err != nil {
		t.Fatalf("update: %v (stdout=%s)", err, stdout)
	}

	var view updateResult
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("JSON デコードに失敗しました: %v (raw=%s)", err, stdout)
	}
	if view.UpdateAvailable || view.Applied {
		t.Errorf("最新なのに更新しようとしている: %+v", view)
	}
}

// TestUpdateNonInteractiveRequiresYes は、cron などの非対話環境で --yes 無しに
// バイナリが黙って入れ替わらないことを確かめる。
// 確認より手前で止まるので、実際のダウンロードは走らない。
func TestUpdateNonInteractiveRequiresYes(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, nil)
	fakeReleaseServer(t, "v9.9.9")

	_, _, err := runCLI(t, "v1.0.0", configPath, "update")
	if err == nil {
		t.Fatal("エラーを期待したが nil だった")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("--yes を促すエラーを期待した: %v", err)
	}
}

// TestUpdateDevBuild は、ソースからビルドしたバイナリを更新対象にしないことを
// 確かめる（開発中のバイナリをリリース版で上書きしない）。
func TestUpdateDevBuild(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, nil)
	_, hits := fakeReleaseServer(t, "v9.9.9")

	_, _, err := runCLI(t, "dev", configPath, "update", "--check")
	if err == nil {
		t.Fatal("エラーを期待したが nil だった")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("開発ビルドなのに %d 回問い合わせている", n)
	}
}

// --- 更新通知 ---

// TestUpdateNoticeOnStderr は、通常のコマンドの後に通知が標準エラーへ出ること、
// および --json の標準出力を汚さないことを確かめる。
func TestUpdateNoticeOnStderr(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, nil)
	fakeReleaseServer(t, "v9.9.9")
	allowUpdateNotice(t)

	stdout, stderr, err := runCLI(t, "v1.0.0", configPath, "config", "doctor", "--json")
	if err != nil {
		t.Fatalf("config doctor: %v (stderr=%s)", err, stderr)
	}

	if !strings.Contains(stderr, "新しいバージョンがあります") || !strings.Contains(stderr, "v9.9.9") {
		t.Errorf("標準エラーに通知が出ていません: %q", stderr)
	}
	if !strings.Contains(stderr, "insights update") {
		t.Errorf("更新方法の案内が出ていません: %q", stderr)
	}
	// 通知が標準出力に混ざると --json のパースが壊れる。
	var view doctorResult
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("標準出力が JSON として壊れています: %v (raw=%s)", err, stdout)
	}

	// 確認結果は設定ファイルの隣に記録される。
	if _, ok := update.LoadCache(tmp); !ok {
		t.Errorf("%s が作られていません", update.CacheFileName)
	}
}

// TestUpdateNoticeSuppressedWhenNotTerminal は、標準エラーが端末でない実行
// （cron / パイプ）では通知しないだけでなく、そもそも問い合わせにも行かないことを
// 確かめる。定期実行のログを汚さないための性質。
func TestUpdateNoticeSuppressedWhenNotTerminal(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, nil)
	_, hits := fakeReleaseServer(t, "v9.9.9")

	_, stderr, err := runCLI(t, "v1.0.0", configPath, "config", "doctor")
	if err != nil {
		t.Fatalf("config doctor: %v", err)
	}
	if strings.Contains(stderr, "新しいバージョンがあります") {
		t.Errorf("非対話なのに通知が出ています: %q", stderr)
	}
	// doctor 自身のバージョン確認で 1 回だけ問い合わせる（通知用には出ない）。
	if n := hits.Load(); n != 1 {
		t.Errorf("問い合わせ回数 = %d, 期待 1（doctor の診断のみ）", n)
	}
}

// TestUpdateCheckDisabled は update.check: false でネットワークに一切出ないことを確かめる。
func TestUpdateCheckDisabled(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, func(cfg *config.Config) {
		cfg.Update.Check = false
	})
	_, hits := fakeReleaseServer(t, "v9.9.9")
	allowUpdateNotice(t)

	_, stderr, err := runCLI(t, "v1.0.0", configPath, "config", "doctor")
	if err != nil {
		t.Fatalf("config doctor: %v", err)
	}
	if strings.Contains(stderr, "新しいバージョンがあります") {
		t.Errorf("check: false なのに通知が出ています: %q", stderr)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("check: false なのに %d 回問い合わせている", n)
	}
}

// TestUpdateNoticeUsesCache は、前回の確認から interval 以内なら記録した結果で
// 済ませ、ネットワークに出ないことを確かめる。
func TestUpdateNoticeUsesCache(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	// doctor 自身は毎回問い合わせるので、キャッシュの効きは skill status で見る。
	writeUpdateTestConfig(t, configPath, nil)
	baseURL, hits := fakeReleaseServer(t, "v9.9.9")
	allowUpdateNotice(t)

	if err := update.SaveCache(tmp, update.Cache{CheckedAt: time.Now(), LatestVersion: "v9.9.9"}); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	skillDir := filepath.Join(tmp, "agent-home")
	t.Setenv("CODEX_HOME", skillDir)

	_, stderr, err := runCLI(t, "v1.0.0", configPath, "skill", "status", "--agent", "codex")
	if err != nil {
		t.Fatalf("skill status: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stderr, "新しいバージョンがあります") {
		t.Errorf("キャッシュから通知が出ていません: %q", stderr)
	}
	if !strings.Contains(stderr, baseURL+"/releases/tag/v9.9.9") {
		t.Errorf("リリースページの URL が出ていません: %q", stderr)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("キャッシュが新しいのに %d 回問い合わせている", n)
	}
}

// TestDoctorVersionSection は doctor がバージョンの状態を出すことを確かめる。
// 通知が出ない非対話の実行（cron）で、新しい版に気付くための口。
func TestDoctorVersionSection(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, nil)
	fakeReleaseServer(t, "v9.9.9")

	stdout, _, err := runCLI(t, "v1.0.0", configPath, "config", "doctor")
	if err != nil {
		t.Fatalf("config doctor: %v", err)
	}
	for _, want := range []string{"バージョン:", "v1.0.0", "v9.9.9", "新しいバージョンがあります"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor の出力に %q がありません:\n%s", want, stdout)
		}
	}
}

// TestDoctorVersionOffline は、更新確認ができない環境でも doctor が落ちないことを
// 確かめる。診断は「今の状態を伝える」ものであり、確認できないこと自体は
// 設定の誤りではない。
func TestDoctorVersionOffline(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, nil)

	// 誰も待ち受けていない宛先へ向ける。
	prev := updateBaseURL
	updateBaseURL = "http://127.0.0.1:1"
	t.Cleanup(func() { updateBaseURL = prev })

	stdout, _, err := runCLI(t, "v1.0.0", configPath, "config", "doctor")
	if err != nil {
		t.Fatalf("config doctor: %v", err)
	}
	if !strings.Contains(stdout, "確認できません") {
		t.Errorf("確認できない旨が出ていません:\n%s", stdout)
	}
	if !strings.Contains(stdout, "総合判定: OK") {
		t.Errorf("更新確認の失敗で診断が落ちています:\n%s", stdout)
	}
}

// TestUpdateCleansLeftovers は、前回の更新で残った退避ファイル（Windows では
// 更新直後に消せない）が、次の実行で片付くことを確かめる。
//
// 通知を出さない実行（非対話・端末でない）でも片付くこと自体が要点なので、
// updateNoticeAllowed は差し替えない。更新した直後の実行は cron であることも多い。
func TestUpdateCleansLeftovers(t *testing.T) {
	execPath, err := update.ExecutablePath()
	if err != nil {
		t.Fatalf("ExecutablePath: %v", err)
	}
	leftover := execPath + ".old"
	if err := os.WriteFile(leftover, []byte("前回の残骸"), 0o644); err != nil {
		t.Skipf("退避ファイルを作れないため確認できません: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(leftover) })

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	writeUpdateTestConfig(t, configPath, func(cfg *config.Config) {
		// ネットワークにも出ない設定で、後始末だけが走ることを見る。
		cfg.Update.Check = false
	})

	if _, _, err := runCLI(t, "v1.0.0", configPath, "config", "doctor"); err != nil {
		t.Fatalf("config doctor: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("退避ファイルが片付いていません: %s", leftover)
	}
}
