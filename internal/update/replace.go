package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// oldSuffix は置き換え時に現行バイナリを退避させる名前の接尾辞。
//
// Windows は実行中のファイルを削除できないが、リネームはできる。そこで
// 「現行を退避 → 新しいものを本来の名前へ」の順で入れ替え、退避したファイルは
// 次回起動時に消す（CleanupLeftovers）。
const oldSuffix = ".old"

// verifyTimeout は置き換え前の試し実行に許す時間。`--version` を出すだけなので短くてよい。
const verifyTimeout = 20 * time.Second

// InstallMethod はこのバイナリがどう導入されたか。更新できるかどうかがこれで決まる。
type InstallMethod string

const (
	// MethodRelease はリリースアセットを配置したもの。自己更新できる。
	MethodRelease InstallMethod = "release"
	// MethodGoInstall は `go install` で入れたもの。GOBIN の管理下にあるため、
	// バイナリを勝手に差し替えず `go install ...@latest` を案内する。
	MethodGoInstall InstallMethod = "go-install"
	// MethodDevBuild はソースからのローカルビルド。バージョンが分からないので何もしない。
	MethodDevBuild InstallMethod = "dev"
)

// DetectInstallMethod は -ldflags で埋め込まれた生の値から導入方法を判定する。
//
// 判定材料は 2 つ。リリースビルドは -X main.version=<tag> が入る一方、
// モジュールのビルド情報は "(devel)" のままになる。`go install pkg@vX.Y.Z` は逆で、
// ldflags が無いので embedded は "dev" のまま、ビルド情報にはタグが入る。
// 両方とも開発版ならローカルビルド。
//
// 引数には ResolveVersion を通す前の値を渡すこと（通した後では両者を区別できない）。
// リリース扱いにするのはタグとして解釈できる値のときだけ。解釈できない値が
// 埋め込まれている場合は、比較の基準が無く更新できないので開発ビルドとして扱う。
func DetectInstallMethod(embedded string) InstallMethod {
	if _, ok := parseVersion(embedded); ok {
		return MethodRelease
	}
	if moduleVersion() != "" {
		return MethodGoInstall
	}
	return MethodDevBuild
}

// GoInstallCommand は go install で入れた利用者に案内するコマンド。
const GoInstallCommand = "go install github.com/fuchigta/insights/cmd/insights@latest"

// ExecutablePath は実行中のバイナリの実体パスを返す。
// シンボリックリンク越しに起動された場合はリンク先を返す（リンクを壊さないため）。
func ExecutablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("実行ファイルのパスを取得できませんでした: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		// 解決できなければ元のパスで続ける（判断材料が減るだけで、間違いではない）。
		return p, nil
	}
	return resolved, nil
}

// CheckWritable は execPath を置き換えられるかを、実際に同じディレクトリへ
// 一時ファイルを作って確かめる。
//
// ダウンロードしてから権限で失敗すると、時間と帯域を無駄にしたうえに
// 「何が悪いのか」が伝わりにくい。先に確かめて、案内付きのエラーで止める。
func CheckWritable(execPath string) error {
	dir := filepath.Dir(execPath)
	f, err := os.CreateTemp(dir, ".insights-update-*")
	if err != nil {
		return fmt.Errorf("%s に書き込めません: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// VerifyBinary は path を `--version` で起こし、出力に want が含まれることを確かめる。
//
// チェックサムは「ダウンロードが壊れていないか」しか見ておらず、
// 別アーキテクチャのアセットを取ってきてしまった場合は素通りする。実際に起動できて
// 期待するバージョンを名乗ることまで確かめてから入れ替える。
func VerifyBinary(ctx context.Context, path, want string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("新しいバイナリを実行できませんでした (%s): %w: %s",
			path, err, strings.TrimSpace(string(out)))
	}
	got := string(out)
	if !strings.Contains(got, want) && !strings.Contains(got, strings.TrimPrefix(want, "v")) {
		return fmt.Errorf("新しいバイナリが期待するバージョン (%s) を名乗りませんでした: %s",
			want, strings.TrimSpace(got))
	}
	return nil
}

// Replace は newPath を execPath へ入れ替える。
//
// 手順は「現行を .old へ退避 → 新しいものを本来の名前へ」。途中で失敗したら
// 退避したものを戻し、元のバイナリを無傷で残す。
func Replace(execPath, newPath string) error {
	old := execPath + oldSuffix

	// 前回の更新で消しきれなかった残骸があると rename が失敗しうるので、先に片付ける。
	_ = os.Remove(old)

	if err := os.Rename(execPath, old); err != nil {
		return fmt.Errorf("現行バイナリの退避に失敗しました (%s): %w", execPath, err)
	}
	if err := os.Rename(newPath, execPath); err != nil {
		// 戻せなければ .old のまま残るので、その旨まで含めて伝える。
		if restoreErr := os.Rename(old, execPath); restoreErr != nil {
			return fmt.Errorf("新しいバイナリの配置に失敗し、元のバイナリの復旧にも失敗しました。"+
				"%s を %s に手で戻してください: %w", old, execPath, err)
		}
		return fmt.Errorf("新しいバイナリの配置に失敗しました (%s): %w", execPath, err)
	}

	// Windows では実行中のため削除に失敗する。次回起動時に CleanupLeftovers が消す。
	_ = os.Remove(old)
	return nil
}

// CleanupLeftovers は前回の更新で残った退避ファイルを消す。失敗しても無視する
// （消せないことに実害は無く、次の機会に消えればよい）。
func CleanupLeftovers(execPath string) {
	if execPath == "" {
		return
	}
	_ = os.Remove(execPath + oldSuffix)
}

// Applied は Apply の結果。
type Applied struct {
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`
}

// tempPattern は一時ファイル名のパターン。
//
// Windows では拡張子を .exe にしておく必要がある。CreateProcess は拡張子の無い
// パスを渡されると .exe を補ってから探すため、拡張子なしの一時ファイルは
// 置き換え前の試し実行（VerifyBinary）で起動できない。
func tempPattern() string {
	if runtime.GOOS == "windows" {
		return ".insights-update-*.exe"
	}
	return ".insights-update-*"
}

// Apply は r.Latest のバイナリをダウンロードし、実行中のバイナリを置き換える。
func (c *Client) Apply(ctx context.Context, r Result) (Applied, error) {
	execPath, err := ExecutablePath()
	if err != nil {
		return Applied{}, err
	}
	return c.ApplyTo(ctx, r, execPath)
}

// ApplyTo は置き換え先を明示して Apply と同じことを行う。
//
// 一時ファイルは置き換え先と同じディレクトリに作る。別のボリューム（/tmp など）に
// 置くと、最後の rename がボリュームを跨いで失敗する。
func (c *Client) ApplyTo(ctx context.Context, r Result, execPath string) (Applied, error) {
	if err := CheckWritable(execPath); err != nil {
		return Applied{}, err
	}

	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, tempPattern())
	if err != nil {
		return Applied{}, fmt.Errorf("一時ファイルの作成に失敗しました (%s): %w", dir, err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()

	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := c.DownloadTo(ctx, r.Latest, tmpPath); err != nil {
		return Applied{}, err
	}
	if err := VerifyBinary(ctx, tmpPath, r.Latest); err != nil {
		return Applied{}, err
	}
	if err := Replace(execPath, tmpPath); err != nil {
		return Applied{}, err
	}

	ok = true
	return Applied{Path: execPath, From: r.Current, To: r.Latest}, nil
}
