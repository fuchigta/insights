package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/spf13/cobra"
)

// runOptions は `insights run` の実行パラメータ。
type runOptions struct {
	Date string // YYYY-MM-DD。空なら今日
	Yes  bool
}

// newRunCommand は `insights run` を組み立てる。ingest -> judge -> daily を順に実行する
// ショートカットで、cron・タスクスケジューラなど非対話環境からの定期実行を想定している。
func newRunCommand() *cobra.Command {
	var opts runOptions

	cmd := &cobra.Command{
		Use:   "run",
		Short: "ingest -> judge -> daily を一括実行する",
		Long: "cron・タスクスケジューラなど非対話環境からの定期実行を想定したショートカット。\n" +
			"ingest は前回取り込み以降の差分のみを取り込む既定モードで実行する。\n" +
			"judge 相当の AI 評価コストが発生するため、--yes の扱いは insights judge と同じ\n" +
			"（対話端末なら確認、非対話環境では --yes が必須）。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Date != "" {
				if _, err := time.Parse(dayLayout, opts.Date); err != nil {
					return fmt.Errorf("--date の形式が不正です（YYYY-MM-DD で指定してください）: %w", err)
				}
			}
			cfg, err := ConfigFromContext(cmd)
			if err != nil {
				return err
			}
			return runAll(cmd, cfg, opts)
		},
	}

	cmd.Flags().StringVar(&opts.Date, "date", "", "対象日 (YYYY-MM-DD)。省略時は今日")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "評価コストの確認なしで実行する（非対話環境では必須）")

	return cmd
}

// stageResult は run の 1 段階分の結果。
type stageResult struct {
	Name  string `json:"name"` // "ingest" | "judge" | "daily"
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// runResult は `insights run` 全体の結果。--json ではこの構造体をそのまま出す。
// 各段階は独立した意味を持つため、途中の段階が失敗しても、それ以前の段階の結果は
// そのまま残す（例: ingest が成功していれば judge が失敗しても取り込み結果は残る）。
type runResult struct {
	Date            string        `json:"date"`
	Stages          []stageResult `json:"stages"`
	Ingest          *ingestResult `json:"ingest,omitempty"`
	Judge           *judgeResult  `json:"judge,omitempty"`
	Daily           *dailyResult  `json:"daily,omitempty"`
	DurationSeconds float64       `json:"duration_seconds"`
}

// runAll は ingest -> judge -> daily を順に実行する。各段階は runIngest/runJudge/runDaily と
// 同じ本体関数（ingestRun/judgeRun/dailyRun）を出力なしで直接呼び、最後にまとめて1回だけ
// 出力する（各段階が個別に PrintResult すると --json の標準出力が壊れるため）。
//
// ある段階が失敗したら、それ以降の段階は実行しない（前提データが崩れている可能性があるため）。
// ただし、失敗より前の段階の結果は runResult にそのまま残る。
func runAll(cmd *cobra.Command, cfg *config.Config, opts runOptions) error {
	start := time.Now()
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	cmd.SetContext(ctx)

	date := opts.Date
	if date == "" {
		date = time.Now().Local().Format(dayLayout)
	}

	result := &runResult{Date: date}
	stderr := cmd.ErrOrStderr()

	finish := func(stageErr error) error {
		result.DurationSeconds = time.Since(start).Seconds()
		if err := PrintResult(cmd, func(w io.Writer) error {
			return renderRunHuman(w, result)
		}, result); err != nil {
			return err
		}
		return stageErr
	}

	// --- 1. ingest（既定モード: 前回取り込み以降の差分） ---
	fmt.Fprintln(stderr, "insights run: ingest を実行します")
	ingestRes, ingestErr := ingestRun(cmd, cfg, ingestOptions{})
	result.Ingest = ingestRes
	result.Stages = append(result.Stages, stageFrom("ingest", ingestErr))
	if ingestErr != nil {
		fmt.Fprintf(stderr, "insights run: ingest 段階が失敗しました: %v\n", ingestErr)
		return finish(fmt.Errorf("run: ingest 段階で失敗しました: %w", ingestErr))
	}

	// --- 2. judge（--yes の扱いは insights judge と同じ） ---
	fmt.Fprintln(stderr, "insights run: judge を実行します")
	judgeRes, judgeErr := judgeRun(cmd, cfg, judgeOptions{Date: date, Yes: opts.Yes})
	result.Judge = judgeRes
	result.Stages = append(result.Stages, stageFrom("judge", judgeErr))
	if judgeErr != nil {
		fmt.Fprintf(stderr, "insights run: judge 段階が失敗しました: %v\n", judgeErr)
		return finish(fmt.Errorf("run: judge 段階で失敗しました: %w", judgeErr))
	}

	// --- 3. daily ---
	fmt.Fprintln(stderr, "insights run: daily を実行します")
	// judge 段階で既に課金確認を通しているため、daily 側の確認は省略する。
	// judge が一部失敗して未評価が残っていた場合、daily が再度確認を求めて
	// 非対話環境で止まってしまうのを防ぐ。
	dailyRes, dailyErr := dailyRun(cmd, cfg, dailyOptions{Date: date, Yes: opts.Yes})
	result.Daily = dailyRes
	result.Stages = append(result.Stages, stageFrom("daily", dailyErr))
	if dailyErr != nil {
		fmt.Fprintf(stderr, "insights run: daily 段階が失敗しました: %v\n", dailyErr)
		return finish(fmt.Errorf("run: daily 段階で失敗しました: %w", dailyErr))
	}

	fmt.Fprintln(stderr, "insights run: 完了")
	return finish(nil)
}

// stageFrom は 1 段階の実行結果を stageResult に変換する。
func stageFrom(name string, err error) stageResult {
	if err != nil {
		return stageResult{Name: name, OK: false, Error: err.Error()}
	}
	return stageResult{Name: name, OK: true}
}

// renderRunHuman は runResult を人間向けに整形して w に書き出す。
func renderRunHuman(w io.Writer, r *runResult) error {
	fmt.Fprintln(w, "=== insights run ===")
	fmt.Fprintf(w, "対象日: %s\n\n", r.Date)

	for _, s := range r.Stages {
		status := "OK"
		if !s.OK {
			status = "失敗: " + s.Error
		}
		fmt.Fprintf(w, "- %s: %s\n", s.Name, status)
	}
	fmt.Fprintln(w)

	if r.Ingest != nil {
		fmt.Fprintf(w, "ingest: 取り込み %d 件（発見 %d 件）\n", r.Ingest.Ingested, r.Ingest.Discovered)
	}
	if r.Judge != nil {
		fmt.Fprintf(w, "judge: 評価 %d 件成功, %d 件失敗（実コスト $%.4f）\n", r.Judge.Evaluated, r.Judge.Failed, r.Judge.ActualCostUSD)
	}
	if r.Daily != nil {
		if r.Daily.NoSessions {
			fmt.Fprintln(w, "daily: その日のセッションが無いためレポート生成をスキップしました")
		} else {
			fmt.Fprintf(w, "daily: %s / %s\n", r.Daily.DailyPath, r.Daily.RetroPath)
		}
	}

	fmt.Fprintf(w, "\n所要時間: %.1fs\n", r.DurationSeconds)
	return nil
}
