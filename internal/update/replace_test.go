package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeVersionEnv が設定されていると、このテストバイナリは「新しい insights」として
// 振る舞い、--version にその値を返して終了する。
const fakeVersionEnv = "INSIGHTS_TEST_FAKE_INSIGHTS_VERSION"

// TestMain は、この go test バイナリ自身を「ダウンロードしてきた新しい insights」として
// 再利用するためのフック（internal/judge/claudecli のテストと同じ手口）。
//
// 置き換えの検証は、実際にプロセスを起こしてみないと意味がない。チェックサムは
// 「壊れていないか」しか見ておらず、別アーキテクチャのバイナリは素通りするからで、
// VerifyBinary はそこを埋めるための試し実行になっている。偽のバイナリを別途ビルド
// するのではなくテストバイナリを配ることで、Windows を含む全 OS で実プロセスの
// 起動・拡張子の扱いまで確かめられる。
func TestMain(m *testing.M) {
	if v := os.Getenv(fakeVersionEnv); v != "" {
		fmt.Printf("insights version %s\n", v)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// selfPath はこのテストバイナリ自身のパス。「新しい insights」として配る中身になる。
func selfPath(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatalf("テストバイナリのパスを取得できません: %v", err)
	}
	return p
}

// execName は OS に応じた実行ファイル名を返す。
func execName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func TestVerifyBinary(t *testing.T) {
	t.Setenv(fakeVersionEnv, "v9.9.9")

	self := selfPath(t)
	if err := VerifyBinary(context.Background(), self, "v9.9.9"); err != nil {
		t.Fatalf("VerifyBinary: %v", err)
	}
	// 別のバージョンを名乗るバイナリは受け付けない。
	if err := VerifyBinary(context.Background(), self, "v1.0.0"); err == nil {
		t.Fatal("バージョン不一致でエラーを期待したが nil だった")
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	execPath := writeTempFile(t, dir, execName("insights"), "古いバイナリ")
	newPath := writeTempFile(t, dir, "new.tmp", "新しいバイナリ")

	if err := Replace(execPath, newPath); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("置き換え後のファイルを読めません: %v", err)
	}
	if string(got) != "新しいバイナリ" {
		t.Errorf("中身が置き換わっていません: %q", string(got))
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Error("一時ファイルが残っている")
	}
}

func TestCleanupLeftovers(t *testing.T) {
	dir := t.TempDir()
	execPath := writeTempFile(t, dir, execName("insights"), "本体")
	old := writeTempFile(t, dir, filepath.Base(execPath)+oldSuffix, "退避したもの")

	CleanupLeftovers(execPath)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("退避ファイルが消えていない")
	}
	if _, err := os.Stat(execPath); err != nil {
		t.Errorf("本体まで消えている: %v", err)
	}
	// 退避ファイルが無い状態で呼んでも壊れないこと（毎回の起動で呼ばれる）。
	CleanupLeftovers(execPath)
}

// TestApplyTo は、確認からダウンロード・検証・置き換えまでを通しで確かめる。
// 配る中身はこのテストバイナリ自身なので、試し実行が本物のプロセス起動になる。
func TestApplyTo(t *testing.T) {
	t.Setenv(fakeVersionEnv, "v9.9.9")

	srv := newReleaseServer(t, "v9.9.9", selfPath(t), "")

	dir := t.TempDir()
	execPath := writeTempFile(t, dir, execName("insights"), "古いバイナリ")

	c := &Client{BaseURL: srv.URL}
	res, err := c.Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.UpdateAvailable {
		t.Fatal("UpdateAvailable = false")
	}

	applied, err := c.ApplyTo(context.Background(), res, execPath)
	if err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	if applied.To != "v9.9.9" || applied.Path != execPath {
		t.Errorf("Applied = %+v", applied)
	}

	// 置き換わったバイナリが実際に起動して、期待するバージョンを名乗ること。
	if err := VerifyBinary(context.Background(), execPath, "v9.9.9"); err != nil {
		t.Errorf("置き換え後のバイナリを検証できません: %v", err)
	}
	assertNoTempLeftovers(t, dir, execPath)
}

// TestApplyToWrongVersion は、起動はできるが期待するバージョンを名乗らない
// バイナリ（＝別アーキテクチャや取り違えに相当）を弾き、元のバイナリを
// 無傷で残すことを確かめる。チェックサムだけでは通ってしまう経路。
func TestApplyToWrongVersion(t *testing.T) {
	t.Setenv(fakeVersionEnv, "v0.0.1")

	srv := newReleaseServer(t, "v9.9.9", selfPath(t), "")

	dir := t.TempDir()
	execPath := writeTempFile(t, dir, execName("insights"), "古いバイナリ")

	c := &Client{BaseURL: srv.URL}
	res, err := c.Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if _, err := c.ApplyTo(context.Background(), res, execPath); err == nil {
		t.Fatal("エラーを期待したが nil だった")
	}
	assertUnchanged(t, execPath, "古いバイナリ")
	assertNoTempLeftovers(t, dir, execPath)
}

// TestApplyToChecksumMismatch は、チェックサムが合わないときに元のバイナリを
// 置き換えないことを確かめる。
func TestApplyToChecksumMismatch(t *testing.T) {
	t.Setenv(fakeVersionEnv, "v9.9.9")

	srv := newReleaseServer(t, "v9.9.9", selfPath(t), strings.Repeat("a", 64))

	dir := t.TempDir()
	execPath := writeTempFile(t, dir, execName("insights"), "古いバイナリ")

	c := &Client{BaseURL: srv.URL}
	res, err := c.Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if _, err := c.ApplyTo(context.Background(), res, execPath); err == nil {
		t.Fatal("エラーを期待したが nil だった")
	}
	assertUnchanged(t, execPath, "古いバイナリ")
	assertNoTempLeftovers(t, dir, execPath)
}

// TestApplyToNoPermission は、置き換え先に書き込めないとき、ダウンロードを始める前に
// 止まることを確かめる（典型は root 所有の /usr/local/bin）。
// Windows のパーミッションは chmod で再現できないため対象外。
func TestApplyToNoPermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows では chmod による書き込み禁止を再現できない")
	}
	if os.Geteuid() == 0 {
		t.Skip("root では書き込み禁止を再現できない")
	}

	dir := t.TempDir()
	execPath := writeTempFile(t, dir, "insights", "古いバイナリ")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// ダウンロードに行かないことを、宛先を用意しないことで示す。
	c := &Client{BaseURL: "http://127.0.0.1:0"}
	_, err := c.ApplyTo(context.Background(), Result{Current: "v1.0.0", Latest: "v9.9.9"}, execPath)
	if err == nil {
		t.Fatal("エラーを期待したが nil だった")
	}
	if !strings.Contains(err.Error(), "書き込めません") {
		t.Errorf("権限のエラーを期待した: %v", err)
	}
	assertUnchanged(t, execPath, "古いバイナリ")
}

func assertUnchanged(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("元のバイナリが読めません (%s): %v", path, err)
	}
	if string(got) != want {
		t.Errorf("元のバイナリが変わっている: %q", string(got))
	}
}

// assertNoTempLeftovers は、置き換え先のディレクトリに一時ファイルや退避ファイルが
// 残っていないことを確かめる。
func assertNoTempLeftovers(t *testing.T, dir, execPath string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ディレクトリを読めません (%s): %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == filepath.Base(execPath) {
			continue
		}
		t.Errorf("余計なファイルが残っている: %s", name)
	}
}
