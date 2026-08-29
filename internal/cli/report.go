// このファイルは `insights report` を実装する。
// AI 呼び出しは行わず、DB に既に保存されている日次ロールアップ（daily コマンドが
// 作る想定）を任意期間分だけ束ねて、自己完結した HTML レポートに変換するだけ。
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/render"
	"github.com/fuchigta/insights/internal/rollup"
	"github.com/spf13/cobra"
)

// reportDateLayout は --from / --to の日付表現。
const reportDateLayout = "2006-01-02"

// reportResult は `insights report` の実行結果全体。--json ではこの構造体をそのまま出す。
type reportResult struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Generated    bool   `json:"generated"`
	OutputPath   string `json:"output_path,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	DaysWithData int    `json:"days_with_data"`
	MissingDays  int    `json:"missing_days"`
	// Message は Generated=false のとき（期間内にロールアップが1件も無いとき）の案内文。
	Message string `json:"message,omitempty"`
}

// newReportCommand は `insights report` を組み立てる。
func newReportCommand() *cobra.Command {
	var (
		fromFlag string
		toFlag   string
		outFlag  string
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "任意期間の HTML レポートを生成する",
		Long: "DB に保存済みの日次ロールアップ（`insights daily` が作る）を --from/--to の期間分だけ束ね、\n" +
			"外部リソースに依存しない単一の HTML ファイルとして書き出す。AI 呼び出しは行わない。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromFlag == "" || toFlag == "" {
				return fmt.Errorf("--from と --to は両方とも必須です（YYYY-MM-DD で指定してください）")
			}
			if _, err := time.Parse(reportDateLayout, fromFlag); err != nil {
				return fmt.Errorf("--from の形式が不正です（YYYY-MM-DD で指定してください）: %w", err)
			}
			if _, err := time.Parse(reportDateLayout, toFlag); err != nil {
				return fmt.Errorf("--to の形式が不正です（YYYY-MM-DD で指定してください）: %w", err)
			}
			if fromFlag > toFlag {
				return fmt.Errorf("--from は --to 以前の日付にしてください（from=%s, to=%s）", fromFlag, toFlag)
			}

			cfg, err := ConfigFromContext(cmd)
			if err != nil {
				return err
			}

			return runReport(cmd, cfg, fromFlag, toFlag, outFlag)
		},
	}

	cmd.Flags().StringVar(&fromFlag, "from", "", "期間の開始日 (YYYY-MM-DD、必須)")
	cmd.Flags().StringVar(&toFlag, "to", "", "期間の終了日 (YYYY-MM-DD、必須)")
	cmd.Flags().StringVar(&outFlag, "out", "", "出力先 HTML ファイルパス（既定: <output.dir>/insights-<from>_<to>.html）")

	return cmd
}

// runReport は report サブコマンドの本体。
//
// 処理の流れ:
//  1. DB から [from, to] のロールアップ（daily_rollups の JSON）を取得する
//  2. 1件も無ければ、HTML を書かずに案内を出して正常終了する（エラーにしない）
//  3. JSON を rollup.Daily にデシリアライズし、期間内に作成された改善提案と合わせて
//     rollup.BuildSeries に渡す
//  4. 欠測日数（暦日数 - データのある日数）を算出し、0 より大きければ警告として出す
//     （欠測日を 0 として捏造することはしない。BuildSeries もその日の Point を作らない）
//  5. render.WriteHTML で書き出し、パスとサイズを表示する
func runReport(cmd *cobra.Command, cfg *config.Config, from, to, outFlag string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}

	db, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.RollupsInRange(from, to)
	if err != nil {
		return fmt.Errorf("ロールアップの取得に失敗しました: %w", err)
	}

	calendarDays, err := calendarDayCount(from, to)
	if err != nil {
		return fmt.Errorf("期間の日数計算に失敗しました: %w", err)
	}

	if len(rows) == 0 {
		result := reportResult{
			From: from, To: to, Generated: false,
			DaysWithData: 0, MissingDays: calendarDays,
			Message: fmt.Sprintf(
				"%s 〜 %s の期間にはまだレポートが生成されていません。`insights daily --date <日付>` で日次ロールアップを生成してから再度お試しください。",
				from, to,
			),
		}
		return PrintResult(cmd, func(w io.Writer) error {
			fmt.Fprintln(w, result.Message)
			return nil
		}, result)
	}

	dailies := make([]*rollup.Daily, 0, len(rows))
	for _, r := range rows {
		var d rollup.Daily
		if err := json.Unmarshal(r.RollupJSON, &d); err != nil {
			return fmt.Errorf("ロールアップ %s のデコードに失敗しました: %w", r.Date, err)
		}
		dailies = append(dailies, &d)
	}

	allActions, err := db.AllActions()
	if err != nil {
		return fmt.Errorf("改善提案の取得に失敗しました: %w", err)
	}
	actions := make([]model.Action, 0, len(allActions))
	for _, a := range allActions {
		if a.CreatedOn >= from && a.CreatedOn <= to {
			actions = append(actions, a)
		}
	}

	series := rollup.BuildSeries(from, to, dailies, actions)

	missingDays := calendarDays - len(rows)
	if missingDays < 0 {
		missingDays = 0
	}

	outPath, err := resolveReportOutPath(cfg, outFlag, from, to)
	if err != nil {
		return err
	}

	if err := render.WriteHTML(outPath, series, render.HTMLOptions{}); err != nil {
		return fmt.Errorf("HTML レポートの書き出しに失敗しました: %w", err)
	}

	var size int64
	if info, statErr := os.Stat(outPath); statErr == nil {
		size = info.Size()
	}

	result := reportResult{
		From: from, To: to, Generated: true,
		OutputPath:   outPath,
		SizeBytes:    size,
		DaysWithData: len(rows),
		MissingDays:  missingDays,
	}

	return PrintResult(cmd, func(w io.Writer) error {
		return renderReportHuman(w, result)
	}, result)
}

// resolveReportOutPath は --out の解決結果を返す。空なら
// <output.dir>/insights-<from>_<to>.html を使う。
func resolveReportOutPath(cfg *config.Config, outFlag, from, to string) (string, error) {
	if outFlag == "" {
		outDir, err := config.ExpandPath(cfg.Output.Dir)
		if err != nil {
			return "", fmt.Errorf("output.dir の解決に失敗しました: %w", err)
		}
		return filepath.Join(outDir, fmt.Sprintf("insights-%s_%s.html", from, to)), nil
	}
	expanded, err := config.ExpandPath(outFlag)
	if err != nil {
		return "", fmt.Errorf("--out の解決に失敗しました: %w", err)
	}
	return expanded, nil
}

// calendarDayCount は [from, to] の暦日数を返す（両端含む）。
func calendarDayCount(from, to string) (int, error) {
	fd, err := time.Parse(reportDateLayout, from)
	if err != nil {
		return 0, err
	}
	td, err := time.Parse(reportDateLayout, to)
	if err != nil {
		return 0, err
	}
	days := int(td.Sub(fd).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	return days, nil
}

// renderReportHuman は reportResult を人間向けに整形して w に書き出す。
func renderReportHuman(w io.Writer, r reportResult) error {
	fmt.Fprintln(w, "=== insights report ===")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "期間: %s 〜 %s\n", r.From, r.To)
	fmt.Fprintf(w, "データのある日数: %d 日\n", r.DaysWithData)
	if r.MissingDays > 0 {
		fmt.Fprintf(w, "警告: 期間内に %d 日分のロールアップが欠測しています（欠測日は 0 として扱わず、データが無い日として除外しています）。\n", r.MissingDays)
		fmt.Fprintln(w, "      `insights daily --date <日付>` で該当日のロールアップを生成すると補えます。")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "出力先: %s (%d bytes)\n", r.OutputPath, r.SizeBytes)
	return nil
}
