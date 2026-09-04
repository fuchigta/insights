// このファイルは `insights update` と、通常のコマンド実行に付く更新通知を実装する。
//
// 方針として、バイナリを勝手に置き換えることはしない。通知は「新しい版が出ている」と
// 伝えるだけで、入れ替えは利用者が `insights update` を叩いたときだけ行う。
// また通知は標準エラーが端末のときにしか出さないため、cron 実行のログを汚さない
// （非対話で気付く口は `insights config doctor` のバージョン欄）。
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/update"
	"github.com/spf13/cobra"
)

const (
	// updateCheckTimeout は裏で走らせる更新確認の上限。
	updateCheckTimeout = 5 * time.Second
	// updateNoticeGrace はコマンド完了後、確認結果を待つ上限。
	// 間に合わなければ黙って捨てる。通常のコマンドの所要時間を伸ばさないことを優先する。
	updateNoticeGrace = time.Second
)

// updateBaseURL は更新確認・ダウンロードの宛先。テストが httptest のサーバへ
// 差し替えるための唯一の穴として変数にしている（差し替えないかぎり挙動は同じ）。
var updateBaseURL = update.DefaultBaseURL

// updateNoticeAllowed は更新通知を出してよいかの判定。既定は「標準エラーが端末」。
// テストが通知の中身を検証できるよう、newJudge と同じく差し替え可能にしている。
var updateNoticeAllowed = func(cmd *cobra.Command) bool {
	return isTerminalWriter(cmd.ErrOrStderr())
}

// updateResult は `insights update` の実行結果全体。--json ではこの構造体をそのまま出す。
type updateResult struct {
	Method          string `json:"method"`
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url,omitempty"`
	Applied         bool   `json:"applied"`
	Path            string `json:"path,omitempty"`
	// Message は置き換えを行わなかった理由・次にすべきことの案内。
	Message string `json:"message,omitempty"`
}

// newUpdateCommand は `insights update` を組み立てる。
func newUpdateCommand(rawVersion string) *cobra.Command {
	var (
		checkOnly bool
		yes       bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "insights 自身を最新のリリースに更新する",
		Long: "GitHub Releases の最新版を確認し、実行中のバイナリを置き換える。\n" +
			"ダウンロードしたバイナリは sha256 で照合し、実際に起動して期待するバージョンを\n" +
			"名乗ることを確かめてから入れ替える。go install で導入した場合は置き換えず、\n" +
			"go install の手順を案内する。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd, rawVersion, checkOnly, yes)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "確認だけ行い、置き換えない")
	cmd.Flags().BoolVar(&yes, "yes", false, "実行前の確認を省略する（非対話環境では必須）")
	return cmd
}

func runUpdate(cmd *cobra.Command, rawVersion string, checkOnly, yes bool) error {
	ctx := cmd.Context()
	if err := ctx.Err(); err != nil {
		return err
	}

	method := update.DetectInstallMethod(rawVersion)
	current := update.ResolveVersion(rawVersion)
	view := updateResult{Method: string(method), Current: current}

	if method == update.MethodDevBuild {
		return fmt.Errorf("開発ビルド（バージョン %s）は更新できません。リリース版のバイナリか go install で導入したものを使ってください", current)
	}

	// 前回の更新で残った退避ファイルを片付ける（Windows では削除が次回に持ち越される）。
	if execPath, err := update.ExecutablePath(); err == nil {
		update.CleanupLeftovers(execPath)
	}

	client := &update.Client{BaseURL: updateBaseURL}
	res, err := client.Check(ctx, current)
	if err != nil {
		return err
	}
	view.Latest = res.Latest
	view.UpdateAvailable = res.UpdateAvailable
	view.ReleaseURL = res.ReleaseURL

	switch {
	case !res.UpdateAvailable:
		view.Message = "すでに最新です。"
		return printUpdateResult(cmd, view)
	case checkOnly:
		view.Message = "更新するには insights update を実行してください。"
		return printUpdateResult(cmd, view)
	case method == update.MethodGoInstall:
		// GOBIN の管理下にあるバイナリを横から差し替えると、go 側が把握している
		// モジュールバージョンと実体がずれる。go install に任せる。
		view.Message = "go install で導入されています。次のコマンドで更新してください: " + update.GoInstallCommand
		return printUpdateResult(cmd, view)
	}

	execPath, err := update.ExecutablePath()
	if err != nil {
		return err
	}
	view.Path = execPath

	// ダウンロードしてから権限で失敗しないよう、先に書き込めるか確かめる。
	if err := update.CheckWritable(execPath); err != nil {
		return fmt.Errorf("%s を置き換える権限がありません: %w\n"+
			"管理者権限で実行する（例: sudo insights update）か、README のインストール手順で入れ直してください", execPath, err)
	}

	if err := confirmUpdate(cmd, res, execPath, yes); err != nil {
		return err
	}

	applied, err := client.Apply(ctx, res)
	if err != nil {
		return err
	}
	view.Applied = true
	view.Path = applied.Path

	// 更新した版を確認済みとして記録し、直後の実行で通知が再び出ないようにする。
	if dir, dirErr := updateCacheDir(cmd); dirErr == nil {
		_ = update.SaveCache(dir, update.Cache{CheckedAt: time.Now(), LatestVersion: res.Latest})
	}

	return printUpdateResult(cmd, view)
}

func printUpdateResult(cmd *cobra.Command, view updateResult) error {
	return PrintResult(cmd, func(w io.Writer) error {
		return renderUpdateHuman(w, view)
	}, view)
}

func renderUpdateHuman(w io.Writer, r updateResult) error {
	fmt.Fprintln(w, "=== insights update ===")
	fmt.Fprintf(w, "現在のバージョン: %s\n", r.Current)
	fmt.Fprintf(w, "最新のバージョン: %s\n", orDash(r.Latest))
	if r.Applied {
		fmt.Fprintf(w, "更新しました: %s → %s\n", r.Current, r.Latest)
		fmt.Fprintf(w, "配置先: %s\n", r.Path)
		return nil
	}
	if r.Message != "" {
		fmt.Fprintln(w, r.Message)
	}
	if r.UpdateAvailable && r.ReleaseURL != "" {
		fmt.Fprintf(w, "変更点: %s\n", r.ReleaseURL)
	}
	return nil
}

// confirmUpdate は置き換え前の確認。課金は発生しないので confirmCost ほど厳密ではないが、
// 「非対話環境では --yes が要る」という約束は揃えている（cron からバイナリが
// 黙って入れ替わることを防ぐため）。
func confirmUpdate(cmd *cobra.Command, res update.Result, execPath string, yes bool) error {
	if yes {
		return nil
	}

	stderr := cmd.ErrOrStderr()
	fmt.Fprintf(stderr, "%s を %s から %s へ置き換えます。\n", execPath, res.Current, res.Latest)
	fmt.Fprintf(stderr, "変更点: %s\n", res.ReleaseURL)

	if !isInteractiveStdin(cmd) {
		return fmt.Errorf("非対話環境（標準入力が端末ではありません）で実行されています。バイナリを置き換えるため --yes を指定してください")
	}

	fmt.Fprint(stderr, "更新しますか？ [y/N]: ")
	reader := bufio.NewReader(cmd.InOrStdin())
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line != "y" && line != "yes" {
		return fmt.Errorf("ユーザーが更新をキャンセルしました")
	}
	return nil
}

// --- 通常のコマンドに付く更新通知 ---

// startUpdateCheck は更新確認を開始し、結果の受け口を state に持たせる。
// PersistentPreRunE から呼ぶ。ここでは待たない。
//
// ネットワークに出るのは「通知を出しうる状況」に限る。端末でない実行（cron）や
// update.check: false では、そもそも問い合わせをしない。
func startUpdateCheck(cmd *cobra.Command, state *cliState) {
	if state == nil || state.config == nil {
		return
	}

	// 前回の更新で残った退避ファイルの後始末。Windows では更新した実行そのものが
	// 自分を消せないため、次の実行だけが片付ける機会になる。更新確認の設定や
	// 端末かどうかとは無関係なので、以降のどの判定よりも先に行う。
	if execPath, err := update.ExecutablePath(); err == nil {
		update.CleanupLeftovers(execPath)
	}

	// insights update は自分で確認するので二重に走らせない。
	if cmd.Name() == "update" {
		return
	}
	if !state.config.Update.Check {
		return
	}
	if update.DetectInstallMethod(state.rawVersion) == update.MethodDevBuild {
		return
	}
	if !updateNoticeAllowed(cmd) {
		return
	}

	dir := filepath.Dir(state.configPath)
	current := update.ResolveVersion(state.rawVersion)
	interval := state.config.Update.Interval.Duration

	ch := make(chan update.Result, 1)

	// 前回の確認から interval 以内なら、記録済みの最新版で即答してネットワークに出ない。
	if cached, ok := update.LoadCache(dir); ok && cached.Fresh(time.Now(), interval) {
		ch <- update.Result{
			Current:         current,
			Latest:          cached.LatestVersion,
			UpdateAvailable: update.IsNewer(cached.LatestVersion, current),
			ReleaseURL:      updateBaseURL + "/releases/tag/" + cached.LatestVersion,
		}
		state.updateCh = ch
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
		defer cancel()

		client := &update.Client{BaseURL: updateBaseURL}
		res, err := client.Check(ctx, current)
		if err != nil {
			// 更新確認は補助機能なので、失敗を利用者に見せない（--verbose でだけ見える）。
			slog.Debug("更新確認に失敗しました", "error", err)
			close(ch)
			return
		}
		if err := update.SaveCache(dir, update.Cache{CheckedAt: time.Now(), LatestVersion: res.Latest}); err != nil {
			slog.Debug("更新確認のキャッシュ書き込みに失敗しました", "error", err)
		}
		ch <- res
	}()
	state.updateCh = ch
}

// finishUpdateCheck は確認結果が間に合っていれば通知を出す。
// PersistentPostRunE から呼ぶ（コマンドが成功したときだけ走るので、
// 失敗の上に通知を重ねない）。
func finishUpdateCheck(cmd *cobra.Command, state *cliState) {
	if state == nil || state.updateCh == nil {
		return
	}

	select {
	case res, ok := <-state.updateCh:
		if !ok || !res.UpdateAvailable {
			return
		}
		printUpdateNotice(cmd.ErrOrStderr(), res, update.DetectInstallMethod(state.rawVersion))
	case <-time.After(updateNoticeGrace):
		// 間に合わなかった。次回の実行で（キャッシュ経由で）出せばよい。
	}
}

// printUpdateNotice は更新通知を書き出す。宛先は標準エラー（--json の標準出力を汚さない）。
func printUpdateNotice(w io.Writer, res update.Result, method update.InstallMethod) {
	how := "insights update"
	if method == update.MethodGoInstall {
		how = update.GoInstallCommand
	}
	fmt.Fprintf(w, "\n新しいバージョンがあります: %s → %s\n", res.Current, res.Latest)
	fmt.Fprintf(w, "  更新: %s\n", how)
	fmt.Fprintf(w, "  変更点: %s\n", res.ReleaseURL)
}

// updateCacheDir は更新確認のキャッシュを置くディレクトリ（設定ファイルと同じ場所）。
func updateCacheDir(cmd *cobra.Command) (string, error) {
	path, err := ConfigPathFromContext(cmd)
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

// isTerminalWriter は出力先が端末かを判定する。テストでは bytes.Buffer などが
// 渡って *os.File にならないため false になる——これは意図的で、テストの出力に
// 通知が混ざらないようにしている（通知を検証するテストは updateNoticeAllowed を差し替える）。
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminalFile(f)
}
