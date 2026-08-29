package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/judge/claudecli"
	"github.com/fuchigta/insights/internal/judge/prompts"
	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/store"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// dayLayout は --date/--from/--to で使う日付表現。ingest.go の sinceDateLayout と同じ形式だが、
// 意味（対象日 vs since 基準時刻）が異なるためこのファイル用に別名を定義する。
const dayLayout = sinceDateLayout

// estimatedCostPerSessionHaikuUSD / estimatedCostPerSessionSonnetUSD は
// 事前確認に使う「1 セッションあたりの評価コスト」の概算値（USD）。
//
// 実測の内訳（claude-haiku-4-5、2026-08-29 時点）:
//   - スキーマ無しの最小呼び出し: 約 $0.0055
//   - --json-schema 付きの最小呼び出し: 約 $0.074
//     （構造化出力はツール経由で実装されており、ツール定義ぶんの固定費が乗る）
//   - 実セッション 1 件の評価（スキーマ無し）: 約 $0.025
//
// 実運用は --json-schema 付きなので、固定費の実測（$0.074）を下回ることはない。
// 安全側に倒して $0.08 を基準とし、claude-sonnet-5 は単価が 3 倍なので約3倍とする。
// セッションの長さ・委譲件数で実費は大きく変動するため、あくまで事前確認用の目安。
//
// なお claude -p は対話セッションのサブスクリプション枠ではなく API 従量枠を
// 消費する。枠は月あたり限られているので、この見積もりは過小に出さないこと。
const (
	estimatedCostPerSessionHaikuUSD  = 0.08
	estimatedCostPerSessionSonnetUSD = estimatedCostPerSessionHaikuUSD * 3
)

// estimateCostPerSession はモデル名から 1 セッションあたりの概算コストを返す。
// 上記の定数コメントのとおり実測値に基づく大まかな概算であり、正確な金額ではない。
func estimateCostPerSession(modelName string) float64 {
	if strings.Contains(strings.ToLower(modelName), "haiku") {
		return estimatedCostPerSessionHaikuUSD
	}
	return estimatedCostPerSessionSonnetUSD
}

// judgeOptions は `insights judge` の実行パラメータ。
type judgeOptions struct {
	Date  string // YYYY-MM-DD。空なら Range か今日
	From  string
	To    string
	Force bool
	Yes   bool
	Limit int
}

// newJudgeCommand は `insights judge` を組み立てる。
func newJudgeCommand() *cobra.Command {
	var opts judgeOptions

	cmd := &cobra.Command{
		Use:   "judge",
		Short: "未評価セッションを AI で評価する",
		Long: "対象期間（--date 単日、または --from/--to。どちらも無ければ今日）の未評価セッションを\n" +
			"AI（claude -p）で評価し、結果を DB にキャッシュする。\n" +
			"サブエージェント（IsSidechain）は個別に評価しない（親セッションの評価に委譲の要約として含める）。\n" +
			"評価には課金が発生するため、対話端末では実行前に確認する。非対話環境では --yes が必須。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Date != "" && (opts.From != "" || opts.To != "") {
				return fmt.Errorf("--date と --from/--to は同時に指定できません")
			}
			if (opts.From == "") != (opts.To == "") {
				return fmt.Errorf("--from と --to は両方指定してください")
			}
			if opts.Limit < 0 {
				return fmt.Errorf("--limit は 0 以上を指定してください")
			}

			cfg, err := ConfigFromContext(cmd)
			if err != nil {
				return err
			}
			return runJudge(cmd, cfg, opts)
		},
	}

	cmd.Flags().StringVar(&opts.Date, "date", "", "対象日 (YYYY-MM-DD)")
	cmd.Flags().StringVar(&opts.From, "from", "", "対象期間の開始日 (YYYY-MM-DD)。--to とセットで指定する")
	cmd.Flags().StringVar(&opts.To, "to", "", "対象期間の終了日 (YYYY-MM-DD)。--from とセットで指定する")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "キャッシュ済みの評価も再実行する")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "確認なしで実行する（非対話環境では必須）")
	cmd.Flags().IntVar(&opts.Limit, "limit", 0, "評価する件数の上限（0 は無制限）")

	return cmd
}

// evalFailure は 1 セッションの評価失敗を表す。
type evalFailure struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

// errEvalAborted はレート制限の検知により、評価を実行せず打ち切ったセッションの理由。
var errEvalAborted = errors.New("レート制限を検知したため評価を実行しませんでした")

// maxShownFailureReasons は失敗理由を stderr に何件まで並べるか。
// 全件出すと対象が多い日にログが失敗理由で埋まるため、代表例だけ見せる。
const maxShownFailureReasons = 3

// reportEvalFailures は評価に失敗したセッションの理由を stderr に出す。
// 件数だけでは原因（レート制限なのか、応答がスキーマに合わないのか）が分からず、
// 利用者は同じ実行を繰り返すしかなくなるため、代表例を必ず見せる。
func reportEvalFailures(stderr io.Writer, r *evalRunResult) {
	if stderr == nil || r == nil || len(r.Failed) == 0 {
		return
	}
	shown := len(r.Failed)
	if shown > maxShownFailureReasons {
		shown = maxShownFailureReasons
	}
	for _, f := range r.Failed[:shown] {
		fmt.Fprintf(stderr, "  - %s: %s\n", f.SessionID, f.Reason)
	}
	if len(r.Failed) > shown {
		fmt.Fprintf(stderr, "  ... 他 %d 件（詳細は --json の failure_details を参照）\n", len(r.Failed)-shown)
	}
	if r.RateLimited {
		fmt.Fprintln(stderr, "insights judge: レート制限が解除されてから再実行してください（成功済みの評価はキャッシュされるため、再実行しても評価し直しにはなりません）")
	}
}

// evalStageError は評価結果を見て「後続の AI 処理に進んでよいか」を判定する。
//
// レート制限で打ち切ったときと、1 件も成功しなかったときはエラーにする。前者は続けても
// 同じ失敗を繰り返すだけで、後者は評価が 1 件も無いまま日報を作ることになり、中身の無い
// 成果物と余計な課金だけが残る。一部だけの失敗は従来どおり続行する。
func evalStageError(r *evalRunResult) error {
	if r == nil {
		return nil
	}
	if r.RateLimited {
		return fmt.Errorf("レート制限のため評価を中止しました（成功 %d 件, 失敗 %d 件）。時間をおいてから再実行してください",
			len(r.Succeeded), len(r.Failed))
	}
	if len(r.Succeeded) == 0 && len(r.Failed) > 0 {
		return fmt.Errorf("評価対象 %d 件がすべて失敗しました: %s", len(r.Failed), r.Failed[0].Reason)
	}
	return nil
}

// judgeResult は `insights judge` の実行結果全体。--json ではこの構造体をそのまま出す。
type judgeResult struct {
	From                string        `json:"from"`
	To                  string        `json:"to"`
	Force               bool          `json:"force"`
	TotalSessions       int           `json:"total_sessions"`
	SidechainExcluded   int           `json:"sidechain_excluded"`
	CacheSkipped        int           `json:"cache_skipped"`
	Targeted            int           `json:"targeted"` // --limit 適用後、実際に評価しようとした件数
	Evaluated           int           `json:"evaluated"`
	Failed              int           `json:"failed"`
	FailureDetails      []evalFailure `json:"failure_details,omitempty"`
	RateLimited         bool          `json:"rate_limited,omitempty"` // レート制限を検知して残りの評価を打ち切ったか
	EstimatedCostUSD    float64       `json:"estimated_cost_usd"`
	ActualCostUSD       float64       `json:"actual_cost_usd"`
	EvaluatedSessionIDs []string      `json:"evaluated_session_ids,omitempty"`
	JudgeRunSessionIDs  []string      `json:"judge_run_session_ids,omitempty"` // claude 実行自体の session_id（集計対象から除外する用）
	DurationSeconds     float64       `json:"duration_seconds"`
}

// runJudge は judge サブコマンドの本体（cobra RunE から呼ばれる）。judgeRun を実行し、
// その結果を（--json かどうかに応じて）出力する。
func runJudge(cmd *cobra.Command, cfg *config.Config, opts judgeOptions) error {
	result, runErr := judgeRun(cmd, cfg, opts)
	if result == nil {
		return runErr
	}
	if err := PrintResult(cmd, func(w io.Writer) error {
		return renderJudgeHuman(w, result)
	}, result); err != nil {
		return err
	}
	return runErr
}

// judgeRun は judge の本体処理を行い、結果を返す（出力はしない）。
// `insights run` / `insights daily` の内部呼び出しでも共有できるよう、出力とコマンド組み立てを分離している。
func judgeRun(cmd *cobra.Command, cfg *config.Config, opts judgeOptions) (*judgeResult, error) {
	start := time.Now()

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	from, to, label, err := resolveJudgeRange(opts)
	if err != nil {
		return nil, err
	}
	result := &judgeResult{From: label.from, To: label.to, Force: opts.Force}

	db, err := openStore(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.SessionsInRange(from, to)
	if err != nil {
		return nil, fmt.Errorf("セッションの取得に失敗しました: %w", err)
	}
	result.TotalSessions = len(rows)

	usageRows, err := db.UsageInRange(from, to)
	if err != nil {
		return nil, fmt.Errorf("usage の取得に失敗しました: %w", err)
	}

	targets, sidechainExcluded, cacheSkipped, childrenByParent, costs, err := prepareEvalTargets(
		db, rows, usageRows, opts.Force, prompts.PromptVersion)
	if err != nil {
		return nil, err
	}
	result.SidechainExcluded = sidechainExcluded
	result.CacheSkipped = cacheSkipped

	if opts.Limit > 0 && len(targets) > opts.Limit {
		targets = targets[:opts.Limit]
	}
	result.Targeted = len(targets)

	stderr := cmd.ErrOrStderr()
	fmt.Fprintf(stderr, "insights judge: 評価対象 %d 件（サブエージェント除外 %d 件, キャッシュ済み除外 %d 件）\n",
		len(targets), sidechainExcluded, cacheSkipped)

	if len(targets) == 0 {
		result.DurationSeconds = time.Since(start).Seconds()
		return result, nil
	}

	estimated := float64(len(targets)) * estimateCostPerSession(cfg.Judge.Model)
	result.EstimatedCostUSD = estimated

	if err := confirmCost(cmd, "評価対象セッション", len(targets), estimated, opts.Yes); err != nil {
		result.DurationSeconds = time.Since(start).Seconds()
		return result, err
	}

	j, err := buildJudge(cfg)
	if err != nil {
		return result, err
	}

	evalResult, evalErr := evaluateSessions(ctx, evalDeps{
		DB:            db,
		Judge:         j,
		Cfg:           cfg,
		Model:         cfg.Judge.Model,
		JudgeName:     j.Name(),
		PromptVersion: prompts.PromptVersion,
		Concurrency:   cfg.Judge.Concurrency,
	}, targets, childrenByParent, costs, stderr)

	result.Evaluated = len(evalResult.Succeeded)
	result.Failed = len(evalResult.Failed)
	result.FailureDetails = evalResult.Failed
	result.RateLimited = evalResult.RateLimited
	result.ActualCostUSD = evalResult.CostUSD
	result.EvaluatedSessionIDs = evalResult.Succeeded
	result.JudgeRunSessionIDs = evalResult.RunSessionIDs
	result.DurationSeconds = time.Since(start).Seconds()

	fmt.Fprintf(stderr, "insights judge: 完了（成功 %d 件, 失敗 %d 件, 実コスト $%.4f）\n",
		result.Evaluated, result.Failed, result.ActualCostUSD)
	reportEvalFailures(stderr, evalResult)

	if evalErr != nil {
		return result, evalErr
	}
	// 全滅・レート制限を「成功」として返すと、`insights run` が judge 段階を OK と見なして
	// daily に進み、daily が同じ未評価セッションをもう一度評価してしまう。
	return result, evalStageError(evalResult)
}

// judgeRangeLabel は judgeResult に表示する期間ラベル。
type judgeRangeLabel struct{ from, to string }

// resolveJudgeRange は --date/--from/--to から評価対象期間を決める。どちらも未指定なら今日。
func resolveJudgeRange(opts judgeOptions) (from, to time.Time, label judgeRangeLabel, err error) {
	switch {
	case opts.Date != "":
		from, to, err = dayRange(opts.Date)
		if err != nil {
			return time.Time{}, time.Time{}, judgeRangeLabel{}, fmt.Errorf("--date の形式が不正です（YYYY-MM-DD で指定してください）: %w", err)
		}
		return from, to, judgeRangeLabel{opts.Date, opts.Date}, nil
	case opts.From != "":
		from, _, err = dayRange(opts.From)
		if err != nil {
			return time.Time{}, time.Time{}, judgeRangeLabel{}, fmt.Errorf("--from の形式が不正です（YYYY-MM-DD で指定してください）: %w", err)
		}
		_, to, err = dayRange(opts.To)
		if err != nil {
			return time.Time{}, time.Time{}, judgeRangeLabel{}, fmt.Errorf("--to の形式が不正です（YYYY-MM-DD で指定してください）: %w", err)
		}
		if from.After(to) {
			return time.Time{}, time.Time{}, judgeRangeLabel{}, fmt.Errorf("--from は --to より前の日付を指定してください")
		}
		return from, to, judgeRangeLabel{opts.From, opts.To}, nil
	default:
		today := time.Now().Local().Format(dayLayout)
		from, to, err = dayRange(today)
		if err != nil {
			return time.Time{}, time.Time{}, judgeRangeLabel{}, err
		}
		return from, to, judgeRangeLabel{today, today}, nil
	}
}

// dayRange は YYYY-MM-DD の 1 日分を [start, end] の time.Time に変換する（ローカルタイム基準）。
// rollup.BuildDaily の日付境界判定と同じくローカルタイムを基準にする。
func dayRange(date string) (start, end time.Time, err error) {
	t, err := time.ParseInLocation(dayLayout, date, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start = t
	end = t.Add(24*time.Hour - time.Nanosecond)
	return start, end, nil
}

// confirmCost は評価コストの事前確認を行う。
//
//   - --yes 指定時は確認をスキップする
//   - 標準入力が端末（対話環境）なら、確認プロンプトを出して y/yes 以外はキャンセル扱いにする
//   - 標準入力が端末でない（cron 等の非対話環境）のに --yes も無ければ、黙って課金しないよう
//     エラーにする
func confirmCost(cmd *cobra.Command, label string, count int, estimatedUSD float64, yes bool) error {
	if yes {
		return nil
	}

	stderr := cmd.ErrOrStderr()
	perSession := 0.0
	if count > 0 {
		perSession = estimatedUSD / float64(count)
	}
	fmt.Fprintf(stderr, "%s: %d 件\n", label, count)
	fmt.Fprintf(stderr, "推定コスト: 約 $%.4f（1 回あたり約 $%.4f の概算。実測値に基づく概算であり、正確な金額ではありません）\n",
		estimatedUSD, perSession)

	if !isInteractiveStdin(cmd) {
		return fmt.Errorf("非対話環境（標準入力が端末ではありません）で実行されています。課金が発生するため --yes を指定してください")
	}

	fmt.Fprint(stderr, "実行しますか？ [y/N]: ")
	reader := bufio.NewReader(cmd.InOrStdin())
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line != "y" && line != "yes" {
		return fmt.Errorf("ユーザーが実行をキャンセルしました")
	}
	return nil
}

// isInteractiveStdin は cmd の標準入力が端末かどうかを判定する。
// テストでは cmd.SetIn に bytes.Reader 等を渡すため *os.File にならず、常に false（非対話）になる
// ——これは意図的で、テストが誤って対話プロンプトで停止することを防ぐ。
func isInteractiveStdin(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// --- 評価対象の絞り込み ---

// sessionCostAgg は 1 セッション分の usage を合算したコスト。
type sessionCostAgg struct {
	CostUSD  float64
	AllKnown bool // false なら単価未登録の usage を含む（過小評価の可能性がある）
}

// aggregateSessionCosts は usage 行をセッション単位のコストに集計する。
func aggregateSessionCosts(rows []store.UsageRow) map[string]*sessionCostAgg {
	out := map[string]*sessionCostAgg{}
	for _, u := range rows {
		agg, ok := out[u.SessionID]
		if !ok {
			agg = &sessionCostAgg{AllKnown: true}
			out[u.SessionID] = agg
		}
		if u.CostKnown {
			agg.CostUSD += u.CostUSD
		} else {
			agg.AllKnown = false
		}
	}
	return out
}

// buildChildSummaries は IsSidechain な行を ParentSessionID ごとにまとめ、
// judge.ChildSummary に変換する。方針どおり、サブエージェントは個別評価せず、
// 親セッションの評価プロンプトに渡す要約としてのみ使う。
func buildChildSummaries(rows []store.SessionRow, costs map[string]*sessionCostAgg) map[string][]judge.ChildSummary {
	out := map[string][]judge.ChildSummary{}
	for _, r := range rows {
		if !r.IsSidechain || strings.TrimSpace(r.ParentSessionID) == "" {
			continue
		}
		duration := r.EndedAt.Sub(r.StartedAt).Minutes()
		if duration < 0 {
			duration = 0
		}
		cs := judge.ChildSummary{
			SessionID:       r.SessionID,
			AgentName:       r.Title,
			DurationMinutes: duration,
			MessageCount:    r.MessageCount,
			ToolErrorCount:  r.ToolErrorCount,
		}
		if agg, ok := costs[r.SessionID]; ok {
			cs.CostUSD = agg.CostUSD
			cs.Priced = agg.AllKnown
		}
		out[r.ParentSessionID] = append(out[r.ParentSessionID], cs)
	}
	return out
}

// prepareEvalTargets は SessionsInRange/UsageInRange の結果から、評価すべきセッション一覧
// （サブエージェントを除外し、force で無ければキャッシュ済みも除外したもの）と、
// 委譲要約・コスト集計を組み立てる。
func prepareEvalTargets(
	db *store.DB,
	rows []store.SessionRow,
	usageRows []store.UsageRow,
	force bool,
	promptVersion string,
) (targets []store.SessionRow, sidechainExcluded, cacheSkipped int, childrenByParent map[string][]judge.ChildSummary, costs map[string]*sessionCostAgg, err error) {
	costs = aggregateSessionCosts(usageRows)
	childrenByParent = buildChildSummaries(rows, costs)

	var candidates []store.SessionRow
	for _, r := range rows {
		if r.IsSidechain {
			sidechainExcluded++
			continue
		}
		candidates = append(candidates, r)
	}

	for _, r := range candidates {
		if !force {
			_, ok, evalErr := db.EvalFor(r.SessionID, promptVersion, r.ContentHash)
			if evalErr != nil {
				return nil, 0, 0, nil, nil, fmt.Errorf("評価キャッシュの確認に失敗しました (%s): %w", r.SessionID, evalErr)
			}
			if ok {
				cacheSkipped++
				continue
			}
		}
		targets = append(targets, r)
	}

	return targets, sidechainExcluded, cacheSkipped, childrenByParent, costs, nil
}

// --- AI 評価の実行 ---

// evalDeps は evaluateSessions が必要とする依存一式。
type evalDeps struct {
	DB            *store.DB
	Judge         judge.Judge // フェイク実装（テスト用）も受け付けられるようインターフェースで受け取る
	Cfg           *config.Config
	Model         string
	JudgeName     string
	PromptVersion string
	Concurrency   int
}

// evalRunResult は evaluateSessions の実行結果。
type evalRunResult struct {
	Succeeded []string
	Failed    []evalFailure
	// RateLimited はレート制限を検知して残りの評価を打ち切ったことを表す。
	// この状態では後続の AI 呼び出しも失敗するため、呼び出し側は先に進まない。
	RateLimited   bool
	CostUSD       float64  // RunInfo から取れた分の合計（claudecli.Judge 以外では 0 のまま）
	RunSessionIDs []string // claude 実行自体の session_id（claudecli.Judge 以外では空のまま）
}

// runInfoJudge は claudecli.Judge が実装する EvaluateRun を表す。judge.Judge インターフェースには
// 含まれないメソッドなので、実装しているかどうかを型アサーションで検出する。
// テスト用フェイクはこれを実装しなくてよく、その場合 RunInfo はゼロ値になる
// （コスト・実行セッションIDは追跡できないが、評価フロー自体はテストできる）。
type runInfoJudge interface {
	EvaluateRun(ctx context.Context, req judge.Request) (json.RawMessage, claudecli.RunInfo, error)
}

// evaluateWithRunInfo は j が runInfoJudge を実装していれば EvaluateRun を、
// そうでなければ judge.Judge.Evaluate を呼ぶ（後者の場合 RunInfo はゼロ値）。
func evaluateWithRunInfo(ctx context.Context, j judge.Judge, req judge.Request) (json.RawMessage, claudecli.RunInfo, error) {
	if rj, ok := j.(runInfoJudge); ok {
		return rj.EvaluateRun(ctx, req)
	}
	// 実装していないバックエンドでは評価コストと実行セッション ID を追跡できない。
	// このツールは「評価そのものが本末転倒になっていないか」を自己監視する前提な
	// ので、黙って $0 として集計すると自己監視が機能しなくなる。将来 Codex など
	// 別バックエンドを足したときに気づけるよう警告を出す。
	warnMissingRunInfoOnce.Do(func() {
		slog.Warn("評価バックエンドが EvaluateRun を実装していないため、評価コストと実行セッション ID を記録できません",
			"backend", j.Name())
	})
	raw, err := j.Evaluate(ctx, req)
	return raw, claudecli.RunInfo{}, err
}

// warnMissingRunInfoOnce は上記の警告をバックエンドごとに何度も出さないための制御。
var warnMissingRunInfoOnce sync.Once

// evaluateOneSession は 1 セッションを評価用プロンプトに整形し、AI 評価を実行して
// 結果 JSON（model.Eval として妥当なことを確認済み）を返す。DB への保存はしない
// （呼び出し側 evaluateSessions が直列に行う）。
func evaluateOneSession(
	ctx context.Context,
	deps evalDeps,
	row store.SessionRow,
	children []judge.ChildSummary,
	costs map[string]*sessionCostAgg,
) (json.RawMessage, claudecli.RunInfo, error) {
	session, err := deps.DB.SessionByID(row.SessionID)
	if err != nil {
		return nil, claudecli.RunInfo{}, fmt.Errorf("セッション本文の取得に失敗しました: %w", err)
	}
	evidenceItems, err := deps.DB.EvidenceFor(row.SessionID)
	if err != nil {
		return nil, claudecli.RunInfo{}, fmt.Errorf("成果物の取得に失敗しました: %w", err)
	}

	var sessionCostUSD float64
	var sessionCostPriced bool
	if agg, ok := costs[row.SessionID]; ok {
		// このセッション自身（子セッションを含まない）の usage から算出したコスト。
		// 子のコストは buildChildSummaries 側で別途 ChildSummary.CostUSD に入っている。
		sessionCostUSD = agg.CostUSD
		sessionCostPriced = agg.AllKnown
	}

	goal := ""
	if deps.Cfg != nil {
		goal = deps.Cfg.GoalFor(session.ProjectPath)
	}

	prompt, err := judge.BuildSessionPrompt(judge.SessionPromptInput{
		Session:           session,
		Evidence:          evidenceItems,
		Goal:              goal,
		Children:          children,
		SessionCostUSD:    sessionCostUSD,
		SessionCostPriced: sessionCostPriced,
	})
	if err != nil {
		return nil, claudecli.RunInfo{}, fmt.Errorf("評価プロンプトの構築に失敗しました: %w", err)
	}

	req := judge.Request{
		System: prompts.SessionEvalPrompt(),
		Prompt: prompt,
		Schema: prompts.SessionEvalSchema(),
		Model:  deps.Model,
	}

	raw, run, err := evaluateWithRunInfo(ctx, deps.Judge, req)
	if err != nil {
		return nil, run, fmt.Errorf("AI 評価の実行に失敗しました: %w", err)
	}

	var ev model.Eval
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, run, fmt.Errorf("評価結果 JSON の妥当性確認に失敗しました: %w", err)
	}

	return raw, run, nil
}

// evaluateSessions は targets を並行に評価し、結果を直列に DB へ保存する。
// 並行度は deps.Concurrency（cfg.Judge.Concurrency）を上限とするが、store は接続を
// 直列化しているため（store.Open が SetMaxOpenConns(1) する）、DB 書き込み自体は
// この関数内の単一 goroutine（結果集約ループ）でのみ行う。
//
// 1 件の評価失敗で全体を止めない。失敗は evalRunResult.Failed に集計し、成功した分は
// 保存したうえで結果を返す。ctx がキャンセルされた場合のみ error を返す（呼び出し側が
// 中断を検知できるようにするため）。
//
// ただしレート制限だけは例外で、検知した時点で残りの評価を打ち切る。レート制限は
// 対象セッションごとの失敗ではなくアカウント全体に効いている状態なので、残りを
// 走らせても失敗が増えるだけ（1 件あたり最大 3 回のリトライぶん時間も食う）。
// 打ち切ったことは evalRunResult.RateLimited で呼び出し側に伝える。
func evaluateSessions(
	ctx context.Context,
	deps evalDeps,
	targets []store.SessionRow,
	childrenByParent map[string][]judge.ChildSummary,
	costs map[string]*sessionCostAgg,
	stderr io.Writer,
) (*evalRunResult, error) {
	result := &evalRunResult{}
	if len(targets) == 0 {
		return result, nil
	}

	// レート制限を検知したときに未着手のジョブを止めるための内部キャンセル。
	// 親 ctx（Ctrl-C）とは区別したいので別に用意する。
	evalCtx, abortEval := context.WithCancel(ctx)
	defer abortEval()

	type outcome struct {
		sessionID string
		raw       json.RawMessage
		run       claudecli.RunInfo
		err       error
	}

	jobs := make(chan store.SessionRow, len(targets))
	outcomes := make(chan outcome, len(targets))

	concurrency := deps.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(targets) {
		concurrency = len(targets)
	}

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range jobs {
				if evalCtx.Err() != nil {
					// 中断理由を取り違えないよう、親 ctx のキャンセル（Ctrl-C）と
					// レート制限による打ち切りを分けて記録する。
					if ctx.Err() != nil {
						outcomes <- outcome{sessionID: row.SessionID, err: ctx.Err()}
					} else {
						outcomes <- outcome{sessionID: row.SessionID, err: errEvalAborted}
					}
					continue
				}
				raw, run, err := evaluateOneSession(evalCtx, deps, row, childrenByParent[row.SessionID], costs)
				if errors.Is(err, claudecli.ErrRateLimited) {
					// 打ち切りは結果集約ループではなくここで行う。集約側に任せると、
					// outcomes が全件ぶんバッファされているぶんワーカーが先に走り切れて
					// しまい、打ち切りが間に合うかどうかが実行速度まかせになる。
					abortEval()
				}
				outcomes <- outcome{sessionID: row.SessionID, raw: raw, run: run, err: err}
			}
		}()
	}
	go func() {
		for _, t := range targets {
			jobs <- t
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(outcomes)
	}()

	bySessionID := make(map[string]store.SessionRow, len(targets))
	for _, t := range targets {
		bySessionID[t.SessionID] = t
	}

	processed := 0
	aborted := 0
	for o := range outcomes {
		// レート制限で打ち切ったぶんを進捗に混ぜると、評価を続けたように見えてしまう。
		// 打ち切った件数は最後にまとめて 1 行で伝える。
		if errors.Is(o.err, errEvalAborted) {
			aborted++
			result.Failed = append(result.Failed, evalFailure{SessionID: o.sessionID, Reason: o.err.Error()})
			continue
		}
		processed++
		if stderr != nil {
			fmt.Fprintf(stderr, "insights judge: %d/%d 件処理済み\n", processed, len(targets))
		}

		if o.err != nil {
			if errors.Is(o.err, claudecli.ErrRateLimited) && !result.RateLimited {
				// 打ち切り自体はワーカー側で済んでいる。ここでは結果への記録と通知だけ行う。
				result.RateLimited = true
				if stderr != nil {
					fmt.Fprintln(stderr, "insights judge: レート制限らしきエラーを検知したため、残りのセッションの評価を打ち切ります")
				}
			}
			result.Failed = append(result.Failed, evalFailure{SessionID: o.sessionID, Reason: o.err.Error()})
			continue
		}

		row := bySessionID[o.sessionID]
		if err := deps.DB.SaveEval(o.sessionID, deps.JudgeName, deps.Model, deps.PromptVersion, row.ContentHash, o.raw,
			store.EvalRun{CostUSD: o.run.CostUSD, SessionID: o.run.SessionID}); err != nil {
			result.Failed = append(result.Failed, evalFailure{SessionID: o.sessionID, Reason: fmt.Sprintf("評価結果の保存に失敗しました: %v", err)})
			continue
		}

		result.Succeeded = append(result.Succeeded, o.sessionID)
		if o.run.CostUSD != 0 {
			result.CostUSD += o.run.CostUSD
		}
		if strings.TrimSpace(o.run.SessionID) != "" {
			result.RunSessionIDs = append(result.RunSessionIDs, o.run.SessionID)
		}
	}

	if aborted > 0 && stderr != nil {
		fmt.Fprintf(stderr, "insights judge: レート制限のため %d 件は評価せずに打ち切りました\n", aborted)
	}

	sort.Strings(result.Succeeded)
	sort.Slice(result.Failed, func(i, j int) bool { return result.Failed[i].SessionID < result.Failed[j].SessionID })
	sort.Strings(result.RunSessionIDs)

	if ctx.Err() != nil {
		return result, fmt.Errorf("judge が中断されました（%d 件はここまでに保存済みです）: %w", len(result.Succeeded), ctx.Err())
	}
	return result, nil
}

// --- 出力 ---

// renderJudgeHuman は judgeResult を人間向けに整形して w に書き出す。
func renderJudgeHuman(w io.Writer, r *judgeResult) error {
	fmt.Fprintln(w, "=== insights judge ===")
	fmt.Fprintln(w)

	if r.From == r.To {
		fmt.Fprintf(w, "対象日: %s\n", r.From)
	} else {
		fmt.Fprintf(w, "対象期間: %s 〜 %s\n", r.From, r.To)
	}
	if r.Force {
		fmt.Fprintln(w, "モード: --force（キャッシュ済みも再評価）")
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "対象セッション（期間内合計）: %d 件\n", r.TotalSessions)
	fmt.Fprintf(w, "除外（サブエージェント）: %d 件\n", r.SidechainExcluded)
	fmt.Fprintf(w, "除外（評価キャッシュ済み）: %d 件\n", r.CacheSkipped)
	fmt.Fprintf(w, "評価対象: %d 件\n", r.Targeted)
	fmt.Fprintln(w)

	if r.Targeted == 0 {
		fmt.Fprintln(w, "評価対象がありません。")
		return nil
	}

	fmt.Fprintf(w, "評価成功: %d 件\n", r.Evaluated)
	fmt.Fprintf(w, "評価失敗: %d 件\n", r.Failed)
	for _, f := range r.FailureDetails {
		fmt.Fprintf(w, "  - %s: %s\n", f.SessionID, f.Reason)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "推定コスト（実行前概算）: $%.4f\n", r.EstimatedCostUSD)
	fmt.Fprintf(w, "実コスト（RunInfo 合計）: $%.4f\n", r.ActualCostUSD)
	if len(r.EvaluatedSessionIDs) > 0 {
		fmt.Fprintf(w, "評価したセッション: %s\n", strings.Join(r.EvaluatedSessionIDs, ", "))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "所要時間: %.1fs\n", r.DurationSeconds)
	return nil
}
