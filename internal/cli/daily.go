package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/judge/prompts"
	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/pricing"
	"github.com/fuchigta/insights/internal/render"
	"github.com/fuchigta/insights/internal/rollup"
	"github.com/fuchigta/insights/internal/store"
	"github.com/spf13/cobra"
)

// recentDaysWindow は rollup.SynthInput.RecentDays に渡す直近日数。
// 振り返り AI に「最近の傾向」を見せるための材料で、長すぎるとプロンプトが肥大化するため
// 目安として1〜2週間程度に絞る。
const recentDaysWindow = 7

// dailyOptions は `insights daily` の実行パラメータ。
// synthesizeCallCount は rollup.Synthesize が行う AI 呼び出しの回数（日報と振り返りで 2 回）。
// 課金確認の見積もりに使う。
const synthesizeCallCount = 2

type dailyOptions struct {
	Date    string // YYYY-MM-DD。空なら今日
	NoJudge bool
	// Yes は評価前の課金確認を省略する。daily は --no-judge が無ければ
	// 内部で AI 評価を走らせるため、judge と同じ確認を通す必要がある。
	Yes bool
}

// newDailyCommand は `insights daily` を組み立てる。
func newDailyCommand() *cobra.Command {
	var opts dailyOptions

	cmd := &cobra.Command{
		Use:   "daily",
		Short: "指定日の日報と振り返りを生成する",
		Long: "指定日（省略時は今日）のセッションを集計し、日報（何を成し遂げたか）と\n" +
			"振り返り（金と時間の行方・改善提案）を Markdown で生成する。\n" +
			"--no-judge が無ければ、生成前にその日の未評価セッションを judge コマンドと同じ処理で評価する。",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := ConfigFromContext(cmd)
			if err != nil {
				return err
			}
			return runDailyCommand(cmd, cfg, opts)
		},
	}

	cmd.Flags().StringVar(&opts.Date, "date", "", "対象日 (YYYY-MM-DD)。省略時は今日")
	cmd.Flags().BoolVar(&opts.NoJudge, "no-judge", false, "未評価セッションの事前 AI 評価をスキップする")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "評価前の課金確認を省略する（非対話環境では必須）")

	return cmd
}

// dailyResult は `insights daily` の実行結果全体。--json ではこの構造体をそのまま出す。
type dailyResult struct {
	Date              string   `json:"date"`
	NoSessions        bool     `json:"no_sessions"`
	SkippedJudge      bool     `json:"skipped_judge"` // --no-judge 指定時 true
	SidechainExcluded int      `json:"sidechain_excluded"`
	JudgeEvaluated    int      `json:"judge_evaluated"`
	JudgeFailed       int      `json:"judge_failed"`
	JudgeCostUSD      float64  `json:"judge_cost_usd"`
	JudgeSessionIDs   []string `json:"judge_session_ids,omitempty"`
	TotalSessions     int      `json:"total_sessions"`
	TotalCostUSD      float64  `json:"total_cost_usd"`
	DailyPath         string   `json:"daily_path,omitempty"`
	RetroPath         string   `json:"retro_path,omitempty"`
	ProposalCount     int      `json:"proposal_count"`
	DurationSeconds   float64  `json:"duration_seconds"`
}

// runDailyCommand は daily サブコマンドの本体（cobra RunE から呼ばれる）。
// dailyRun を実行し、その結果を（--json かどうかに応じて）出力する。
func runDailyCommand(cmd *cobra.Command, cfg *config.Config, opts dailyOptions) error {
	result, runErr := dailyRun(cmd, cfg, opts)
	if result == nil {
		return runErr
	}
	if err := PrintResult(cmd, func(w io.Writer) error {
		return renderDailyHuman(w, result)
	}, result); err != nil {
		return err
	}
	return runErr
}

// dailyRun は daily の入力（DB オープン・claude-cli の判定バックエンド構築）を組み立て、
// runDaily（テスト可能な本体）を呼ぶ。`insights run` からもこちらを直接使う。
//
// その日にセッションが 1 件も無ければ、AI（judge/rollup.Synthesize いずれも）を一切呼ばずに
// 早期リターンする（buildJudge すら呼ばない。claude 実行ファイルが無い環境でも
// 空振りの daily 実行は必ず成功させるため）。
func dailyRun(cmd *cobra.Command, cfg *config.Config, opts dailyOptions) (*dailyResult, error) {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	date := opts.Date
	if date == "" {
		date = time.Now().Local().Format(dayLayout)
	} else if _, err := time.Parse(dayLayout, date); err != nil {
		return nil, fmt.Errorf("--date の形式が不正です（YYYY-MM-DD で指定してください）: %w", err)
	}

	db, err := openStore(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		return nil, fmt.Errorf("日付の解決に失敗しました: %w", err)
	}

	rows, err := db.SessionsInRange(dayStart, dayEnd)
	if err != nil {
		return nil, fmt.Errorf("セッションの取得に失敗しました: %w", err)
	}

	if len(rows) == 0 {
		return &dailyResult{Date: date, NoSessions: true}, nil
	}

	prices, err := buildPriceTable(cfg)
	if err != nil {
		return nil, err
	}

	// 日報・振り返りの生成（rollup.Synthesize）は --no-judge の有無に関わらず必ず AI を使うため、
	// セッションが 1 件でもあればここで judge バックエンドを構築する。
	j, err := buildJudge(cfg)
	if err != nil {
		return nil, err
	}

	return runDaily(ctx, cmd, cfg, db, prices, j, rows, date, opts.NoJudge, opts.Yes)
}

// runDaily は daily の本体処理。judge.Judge をパラメータで受け取るため、
// テストではフェイク実装を渡して claude を実際に呼ばずに検証できる。
func runDaily(
	ctx context.Context,
	cmd *cobra.Command,
	cfg *config.Config,
	db *store.DB,
	prices *pricing.Table,
	j judge.Judge,
	rows []store.SessionRow,
	date string,
	noJudge bool,
	yes bool,
) (*dailyResult, error) {
	start := time.Now()
	stderr := cmd.ErrOrStderr()

	result := &dailyResult{Date: date}

	dayStart, dayEnd, err := dayRange(date)
	if err != nil {
		return nil, fmt.Errorf("日付の解決に失敗しました: %w", err)
	}

	usageRows, err := db.UsageInRange(dayStart, dayEnd)
	if err != nil {
		return nil, fmt.Errorf("usage の取得に失敗しました: %w", err)
	}

	// daily は 2 種類の AI 呼び出しを行う:
	//   (a) 未評価セッションの評価（--no-judge で抑止できる）
	//   (b) 日報と振り返りの生成（rollup.Synthesize、2 回。--no-judge でも必ず走る）
	// (b) を確認の対象から外すと「--no-judge なら課金しない」と誤解されるため、
	// 両方をまとめてここで 1 回だけ確認する。
	pendingEvals := 0
	if !noJudge {
		pre, _, _, _, _, prepErr := prepareEvalTargets(db, rows, usageRows, false, prompts.PromptVersion)
		if prepErr != nil {
			return nil, prepErr
		}
		pendingEvals = len(pre)
	}
	aiCalls := pendingEvals + synthesizeCallCount
	label := fmt.Sprintf("AI 呼び出し（セッション評価 %d 件 + 日報・振り返りの生成 %d 回）", pendingEvals, synthesizeCallCount)
	if err := confirmCost(cmd, label, aiCalls, float64(aiCalls)*estimateCostPerSession(cfg.Judge.Model), yes); err != nil {
		return nil, err
	}

	var judgeCostUSD float64
	var judgeSessionIDs []string

	if noJudge {
		result.SkippedJudge = true
		// サブエージェント件数の表示のためだけに絞り込みを行う（評価は実行しない）。
		_, sidechainExcluded, _, _, _, prepErr := prepareEvalTargets(db, rows, usageRows, false, prompts.PromptVersion)
		if prepErr == nil {
			result.SidechainExcluded = sidechainExcluded
		}
	} else {
		targets, sidechainExcluded, _, childrenByParent, costs, prepErr := prepareEvalTargets(db, rows, usageRows, false, prompts.PromptVersion)
		if prepErr != nil {
			return nil, prepErr
		}
		result.SidechainExcluded = sidechainExcluded

		if len(targets) > 0 {
			fmt.Fprintf(stderr, "insights daily: 未評価セッション %d 件を評価します\n", len(targets))
			evalResult, evalErr := evaluateSessions(ctx, evalDeps{
				DB:            db,
				Judge:         j,
				Cfg:           cfg,
				Model:         cfg.Judge.Model,
				JudgeName:     j.Name(),
				PromptVersion: prompts.PromptVersion,
				Concurrency:   cfg.Judge.Concurrency,
			}, targets, childrenByParent, costs, stderr)

			if evalResult != nil {
				result.JudgeEvaluated = len(evalResult.Succeeded)
				result.JudgeFailed = len(evalResult.Failed)
				judgeCostUSD = evalResult.CostUSD
				judgeSessionIDs = evalResult.RunSessionIDs
				if len(evalResult.Failed) > 0 {
					fmt.Fprintf(stderr, "insights daily: 評価に失敗したセッションが %d 件あります\n", len(evalResult.Failed))
					reportEvalFailures(stderr, evalResult)
				}
			}
			// ctx キャンセル（Ctrl-C）のときだけ daily 全体を中断する。個々の評価失敗では続行する。
			if evalErr != nil && ctx.Err() != nil {
				result.JudgeCostUSD = judgeCostUSD
				result.JudgeSessionIDs = judgeSessionIDs
				result.DurationSeconds = time.Since(start).Seconds()
				return result, evalErr
			}
			// レート制限中、あるいは評価が 1 件も成功しなかった状態では、続く日報・振り返りの
			// 生成（AI 呼び出し 2 回）もまず失敗する。ここで打ち切って、確実に失敗する呼び出しと
			// 中身の無い成果物を作らない。
			if stageErr := evalStageError(evalResult); stageErr != nil {
				result.JudgeCostUSD = judgeCostUSD
				result.JudgeSessionIDs = judgeSessionIDs
				result.DurationSeconds = time.Since(start).Seconds()
				return result, fmt.Errorf("%w。評価なしで日報だけ作る場合は --no-judge を指定してください", stageErr)
			}
		}
	}

	sessionsData, err := buildSessionData(db, rows, usageRows)
	if err != nil {
		return nil, err
	}

	daily, err := rollup.BuildDaily(rollup.DailyInput{
		Date:          date,
		Sessions:      sessionsData,
		Prices:        prices,
		Goals:         cfg.GoalFor,
		PromptVersion: prompts.PromptVersion,
		// 丸めのしきい値は設定から渡す。ゼロ値なら集計側の既定値が使われる。
		RollupThreshold: rollup.RollupThreshold{
			CostShare:       cfg.Report.Rollup.CostShare,
			DurationMinutes: cfg.Report.Rollup.DurationMinutes,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("日次集計に失敗しました: %w", err)
	}

	// 評価コストは「この daily 実行の中で評価した分」ではなく、DB に残っている評価から引き直す。
	// judge を先に済ませてから日報を作る経路（`insights run` を含む）ではこの実行での評価は
	// 0 件になり、その場の集計だけでは meta.judge_cost_usd が常に 0 になってしまうため。
	evalCostUSD, evalRunSessionIDs, err := db.EvalRunTotals(sessionIDsOf(rows), prompts.PromptVersion)
	if err != nil {
		return nil, fmt.Errorf("評価コストの集計に失敗しました: %w", err)
	}
	daily.Meta.JudgeCostUSD = evalCostUSD
	daily.Meta.JudgeSessionIDs = evalRunSessionIDs

	openActions, err := db.ActionsByStatus(model.ActionOpen)
	if err != nil {
		return nil, fmt.Errorf("未決着の提案取得に失敗しました: %w", err)
	}

	recentDailies, err := loadRecentDailies(db, date, recentDaysWindow)
	if err != nil {
		return nil, fmt.Errorf("直近日ロールアップの取得に失敗しました: %w", err)
	}

	if err := rollup.Synthesize(ctx, j, daily, rollup.SynthInput{
		GlobalGoal:  cfg.Goals.Global,
		OpenActions: openActions,
		Model:       cfg.Judge.Model,
		RecentDays:  recentDailies,
	}); err != nil {
		return nil, fmt.Errorf("日報・振り返りの生成に失敗しました: %w", err)
	}

	if err := rollup.PersistRetro(db, date, &daily.Retro); err != nil {
		return nil, fmt.Errorf("改善提案の反映に失敗しました: %w", err)
	}

	outDir, err := config.ExpandPath(cfg.Output.Dir)
	if err != nil {
		return nil, fmt.Errorf("output.dir の解決に失敗しました: %w", err)
	}

	dailyPath, retroPath, err := render.WriteReports(outDir, daily)
	if err != nil {
		return nil, err
	}

	rollupJSON, err := json.Marshal(daily)
	if err != nil {
		return nil, fmt.Errorf("ロールアップのシリアライズに失敗しました: %w", err)
	}
	if err := db.SaveRollup(date, rollupJSON, dailyPath, retroPath); err != nil {
		return nil, fmt.Errorf("ロールアップの保存に失敗しました: %w", err)
	}

	result.TotalSessions = daily.Totals.Sessions
	result.TotalCostUSD = daily.Totals.CostUSD
	result.JudgeCostUSD = judgeCostUSD
	result.JudgeSessionIDs = judgeSessionIDs
	result.DailyPath = dailyPath
	result.RetroPath = retroPath
	result.ProposalCount = len(daily.Retro.Proposals)
	result.DurationSeconds = time.Since(start).Seconds()

	fmt.Fprintf(stderr, "insights daily: 完了（daily=%s, retro=%s）\n", dailyPath, retroPath)

	return result, nil
}

// buildSessionData は SessionsInRange/UsageInRange の結果と評価キャッシュ・成果物件数から
// rollup.BuildDaily への入力（rollup.SessionData の列）を組み立てる。
func buildSessionData(db *store.DB, rows []store.SessionRow, usageRows []store.UsageRow) ([]rollup.SessionData, error) {
	usageBySession := map[string][]store.UsageRow{}
	for _, u := range usageRows {
		usageBySession[u.SessionID] = append(usageBySession[u.SessionID], u)
	}

	out := make([]rollup.SessionData, 0, len(rows))
	for _, r := range rows {
		var ev *model.Eval
		raw, ok, err := db.EvalFor(r.SessionID, prompts.PromptVersion, r.ContentHash)
		if err != nil {
			return nil, fmt.Errorf("評価キャッシュの取得に失敗しました (%s): %w", r.SessionID, err)
		}
		if ok {
			var e model.Eval
			if err := json.Unmarshal(raw, &e); err != nil {
				return nil, fmt.Errorf("評価結果のパースに失敗しました (%s): %w", r.SessionID, err)
			}
			ev = &e
		}

		evidenceItems, err := db.EvidenceFor(r.SessionID)
		if err != nil {
			return nil, fmt.Errorf("成果物の取得に失敗しました (%s): %w", r.SessionID, err)
		}

		out = append(out, rollup.SessionData{
			Row:      r,
			Usage:    usageBySession[r.SessionID],
			Eval:     ev,
			Evidence: len(evidenceItems),
		})
	}
	return out, nil
}

// loadRecentDailies は date より前の直近 days 日分の Daily を daily_rollups から復元する。
// 当日自身（まだ生成中）は含めない。
func loadRecentDailies(db *store.DB, date string, days int) ([]*rollup.Daily, error) {
	to, err := time.Parse(dayLayout, date)
	if err != nil {
		return nil, fmt.Errorf("日付のパースに失敗しました: %w", err)
	}
	from := to.AddDate(0, 0, -days)

	rows, err := db.RollupsInRange(from.Format(dayLayout), date)
	if err != nil {
		return nil, fmt.Errorf("daily_rollups の取得に失敗しました: %w", err)
	}

	var out []*rollup.Daily
	for _, r := range rows {
		if r.Date == date {
			continue
		}
		var d rollup.Daily
		if err := json.Unmarshal(r.RollupJSON, &d); err != nil {
			return nil, fmt.Errorf("daily_rollups(%s) のパースに失敗しました: %w", r.Date, err)
		}
		out = append(out, &d)
	}
	return out, nil
}

// sessionIDsOf は集計対象セッションの ID だけを取り出す。
func sessionIDsOf(rows []store.SessionRow) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.SessionID)
	}
	return ids
}

// --- 出力 ---

// renderDailyHuman は dailyResult を人間向けに整形して w に書き出す。
func renderDailyHuman(w io.Writer, r *dailyResult) error {
	fmt.Fprintf(w, "=== insights daily (%s) ===\n\n", r.Date)

	if r.NoSessions {
		fmt.Fprintln(w, "その日のセッションがありません。日報・振り返りは生成しませんでした。")
		return nil
	}

	if r.SkippedJudge {
		fmt.Fprintln(w, "評価: --no-judge によりスキップしました")
	} else {
		fmt.Fprintf(w, "評価: 成功 %d 件, 失敗 %d 件（サブエージェント除外 %d 件）\n", r.JudgeEvaluated, r.JudgeFailed, r.SidechainExcluded)
		fmt.Fprintf(w, "評価コスト（今回発生分）: $%.4f\n", r.JudgeCostUSD)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "対象セッション: %d 件\n", r.TotalSessions)
	fmt.Fprintf(w, "合計コスト: $%.4f\n", r.TotalCostUSD)
	fmt.Fprintf(w, "新規改善提案: %d 件\n", r.ProposalCount)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "日報: %s\n", r.DailyPath)
	fmt.Fprintf(w, "振り返り: %s\n", r.RetroPath)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "所要時間: %.1fs\n", r.DurationSeconds)
	return nil
}
