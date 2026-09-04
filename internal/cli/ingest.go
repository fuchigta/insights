package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/evidence"
	"github.com/fuchigta/insights/internal/model"
	"github.com/fuchigta/insights/internal/pricing"
	"github.com/fuchigta/insights/internal/source"
	"github.com/fuchigta/insights/internal/source/claudecode"
	"github.com/fuchigta/insights/internal/source/codex"
	"github.com/fuchigta/insights/internal/store"
	"github.com/spf13/cobra"
)

// sinceDateLayout は --since フラグの日付表現。
const sinceDateLayout = "2006-01-02"

// ingestOptions は `insights ingest` の実行パラメータ。
type ingestOptions struct {
	Since      time.Time // ゼロ値なら --since 未指定
	All        bool
	DryRun     bool
	NoEvidence bool
}

// newIngestCommand は `insights ingest` を組み立てる。
func newIngestCommand() *cobra.Command {
	var (
		sinceFlag  string
		all        bool
		dryRun     bool
		noEvidence bool
	)

	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "セッションログを取り込む",
		Long: "Claude Code などのセッションログを発見して正規化し、DB に取り込む。\n" +
			"既定では前回の取り込み以降の差分のみを取り込み、初回実行時は全件取り込む。\n" +
			"取り込み済みのファイルは mtime/size が変わらない限り再取り込みしない（冪等）。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if sinceFlag != "" && all {
				return fmt.Errorf("--since と --all は同時に指定できません")
			}

			opts := ingestOptions{All: all, DryRun: dryRun, NoEvidence: noEvidence}
			if sinceFlag != "" {
				t, err := time.Parse(sinceDateLayout, sinceFlag)
				if err != nil {
					return fmt.Errorf("--since の形式が不正です（YYYY-MM-DD で指定してください）: %w", err)
				}
				opts.Since = t
			}

			cfg, err := ConfigFromContext(cmd)
			if err != nil {
				return err
			}

			return runIngest(cmd, cfg, opts)
		},
	}

	cmd.Flags().StringVar(&sinceFlag, "since", "", "指定日 (YYYY-MM-DD) 以降に更新されたトランスクリプトのみ取り込む")
	cmd.Flags().BoolVar(&all, "all", false, "全件取り込む（--since と同時指定不可）")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "取り込み対象を数えるだけで DB に書き込まない")
	cmd.Flags().BoolVar(&noEvidence, "no-evidence", false, "成果物（git commit・PR/Issue/MR）の収集をスキップする")

	return cmd
}

// judgeWorkspaceDirName は claudecli.Judge の既定作業ディレクトリ名。
// internal/judge/claudecli の resolveWorkDir（~/.insights/judge-workspace）と
// 必ず一致させること。ここが評価自体のセッションログの置き場になるため、
// 放置すると評価の実行自体が集計対象を汚染し続ける。
const judgeWorkspaceDirName = "judge-workspace"

// judgeWorkspacePath は評価バックエンド既定の作業ディレクトリパスを返す。
// ユーザー設定に依存させたくない（設定を書き忘れても必ず除外されてほしい）ため、
// config.Config を経由せず直接 os.UserHomeDir() から組み立てる。
func judgeWorkspacePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ホームディレクトリの取得に失敗しました: %w", err)
	}
	return filepath.Join(home, ".insights", judgeWorkspaceDirName), nil
}

// isUnderJudgeWorkspace は projectPath が評価ワークスペース（workspace）配下かどうかを判定する。
// config.Config.ExcludesProject と同様、比較前にパスを正規化し、Windows のパス区切り・
// 大文字小文字の差を吸収する。
func isUnderJudgeWorkspace(projectPath, workspace string) bool {
	if strings.TrimSpace(projectPath) == "" || strings.TrimSpace(workspace) == "" {
		return false
	}
	target := normalizePathForCompare(projectPath)
	ws := normalizePathForCompare(workspace)
	if strings.EqualFold(target, ws) {
		return true
	}
	// 配下判定: ws + セパレータ で始まるか（大文字小文字を無視）。
	prefix := strings.ToLower(ws) + string(filepath.Separator)
	return strings.HasPrefix(strings.ToLower(target), prefix)
}

// normalizePathForCompare はパス比較用に ~ 展開 + Clean を行う。展開に失敗した場合は
// 元の文字列を Clean するだけに留める（config.normalizeForCompare と同じ方針）。
func normalizePathForCompare(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	expanded, err := config.ExpandPath(p)
	if err != nil {
		expanded = p
	}
	return filepath.Clean(expanded)
}

// evidenceSummary は成果物収集の結果集計。
type evidenceSummary struct {
	Enabled          bool     `json:"enabled"`
	AvailableMethods []string `json:"available_methods,omitempty"`
	Commits          int      `json:"commits"`
	PullRequests     int      `json:"pull_requests"`
	Issues           int      `json:"issues"`
	MergeRequests    int      `json:"merge_requests"`
}

// ingestResult は `insights ingest` の実行結果全体。--json ではこの構造体をそのまま出す。
type ingestResult struct {
	Mode                      string          `json:"mode"` // "all" | "since" | "incremental"
	Since                     string          `json:"since,omitempty"`
	DryRun                    bool            `json:"dry_run"`
	Discovered                int             `json:"discovered"`
	Ingested                  int             `json:"ingested"`
	SkippedUpToDate           int             `json:"skipped_up_to_date"`
	SkippedExcludedProject    int             `json:"skipped_excluded_project"`
	SkippedExcludedEntrypoint int             `json:"skipped_excluded_entrypoint"`
	SkippedJudgeWorkspace     int             `json:"skipped_judge_workspace"`
	ParseFailed               int             `json:"parse_failed"`
	ParseFailures             []string        `json:"parse_failures,omitempty"`
	TotalInputTokens          int             `json:"total_input_tokens"`
	TotalOutputTokens         int             `json:"total_output_tokens"`
	TotalCacheReadTokens      int             `json:"total_cache_read_tokens"`
	TotalCacheCreationTokens  int             `json:"total_cache_creation_tokens"`
	EstimatedCostUSD          float64         `json:"estimated_cost_usd"`
	UnknownModels             []string        `json:"unknown_models,omitempty"`
	Evidence                  evidenceSummary `json:"evidence"`
	Interrupted               bool            `json:"interrupted,omitempty"`
	DurationSeconds           float64         `json:"duration_seconds"`
}

// runIngest は ingest サブコマンドの本体（cobra RunE から呼ばれる）。ingestRun を実行し、
// その結果を（--json かどうかに応じて）出力する。
func runIngest(cmd *cobra.Command, cfg *config.Config, opts ingestOptions) error {
	result, runErr := ingestRun(cmd, cfg, opts)
	if result == nil {
		// db オープン失敗など、result を作る前に失敗したケース。出力するものが無い。
		return runErr
	}
	if err := PrintResult(cmd, func(w io.Writer) error {
		return renderIngestHuman(w, result)
	}, result); err != nil {
		return err
	}
	return runErr
}

// ingestRun は ingest の本体処理を行い、結果を返す（出力はしない）。
// `insights run` から呼ぶときは、出力を二重にしないためこちらを直接使う。
//
// 処理の流れ:
//  1. DB を開き、増分取り込みの基準時刻（--since/--all/前回取り込み以降）を決める
//  2. 有効なソース（claude-code / codex）で Discover する
//  3. 発見した Ref を並行にパースし（NeedsIngest で不要な分は事前にスキップ）、
//     結果をチャネル経由で 1 つの goroutine（このループ自身）に集約する。
//     チャネルからの到着順は並行実行のため不定になり得るが、Discover 順（index）で
//     揃うまでバッファし、揃った順に処理するため、実際の SQLite への書き込み・
//     集計・進捗表示は Discover 順で決定的に行われる。
//  4. 除外判定（除外プロジェクト・除外 entrypoint・評価ワークスペース）・コスト算出をしたうえで
//     SaveSession/MarkIngested を直列に呼ぶ
//  5. サブエージェントを除く各セッションについて成果物（evidence）を収集する。
//     git commit はセッションごとに呼ぶが、gh/glab（リモート API）は
//     (プロジェクトパス, 日付) 単位でメモ化し、同じリポジトリ・同じ日なら 1 回しか呼ばない。
func ingestRun(cmd *cobra.Command, cfg *config.Config, opts ingestOptions) (*ingestResult, error) {
	start := time.Now()

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	dbPath, err := config.ExpandPath(cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("db パスの解決に失敗しました: %w", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("DB のオープンに失敗しました (%s): %w", dbPath, err)
	}
	defer db.Close()

	since, mode, sinceLabel, err := resolveSince(db, opts)
	if err != nil {
		return nil, err
	}

	rates := convertPricingOverrides(cfg.Pricing.Overrides)
	priceTable, err := pricing.Load(rates)
	if err != nil {
		return nil, fmt.Errorf("単価表の読み込みに失敗しました: %w", err)
	}

	// 評価バックエンド既定の作業ディレクトリ。放置すると評価の実行自体が
	// 集計対象を汚染し続けるため、ユーザー設定に関係なく常に除外する。
	judgeWorkspace, err := judgeWorkspacePath()
	if err != nil {
		return nil, err
	}

	sources, skippedSources, err := buildSources(cfg)
	if err != nil {
		return nil, err
	}
	for _, s := range skippedSources {
		// 「取り込み対象が 0 件」の理由が分からないと利用者は設定を疑いようがない。
		fmt.Fprintf(cmd.ErrOrStderr(), "insights ingest: ログ置き場が見つからないソースを飛ばしました: %s\n", s)
	}
	sinceBySource, err := sourceSinceMap(db, sources, since, mode)
	if err != nil {
		return nil, err
	}
	refs, err := discoverRefs(sources, sinceBySource)
	if err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })

	result := &ingestResult{Mode: mode, Since: sinceLabel, DryRun: opts.DryRun, Discovered: len(refs)}

	fmt.Fprintf(cmd.ErrOrStderr(), "insights ingest: %d 件のトランスクリプトを検出しました\n", len(refs))

	var gitCollector, forgeCollector *evidence.Collector
	if !opts.NoEvidence && !opts.DryRun {
		result.Evidence.Enabled = true
		result.Evidence.AvailableMethods = evidence.New(cfg.Evidence).Available()
		gitCollector = evidence.New(gitOnlyEvidenceConfig(cfg.Evidence))
		forgeCollector = evidence.New(forgeOnlyEvidenceConfig(cfg.Evidence))
	}

	// --- 並行パース ---
	type parseOutcome struct {
		index       int
		ref         source.Ref
		sess        *model.Session
		err         error
		needsIngest bool
	}

	jobs := make(chan int, len(refs))
	resultsCh := make(chan parseOutcome, len(refs))

	numWorkers := workerCount(len(refs))
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				ref := refs[idx]

				if ctx.Err() != nil {
					resultsCh <- parseOutcome{index: idx, ref: ref, err: ctx.Err()}
					continue
				}

				needs, err := db.NeedsIngest(ref.Source, ref.Path, ref.ModTime, ref.Size)
				if err != nil {
					resultsCh <- parseOutcome{index: idx, ref: ref, err: fmt.Errorf("取り込み状態の確認に失敗しました: %w", err)}
					continue
				}
				if !needs {
					resultsCh <- parseOutcome{index: idx, ref: ref, needsIngest: false}
					continue
				}

				src, ok := sources[ref.Source]
				if !ok {
					resultsCh <- parseOutcome{index: idx, ref: ref, err: fmt.Errorf("未知のソースです: %s", ref.Source)}
					continue
				}
				sess, err := src.Parse(ref)
				resultsCh <- parseOutcome{index: idx, ref: ref, sess: sess, err: err, needsIngest: true}
			}
		}()
	}
	go func() {
		for i := range refs {
			jobs <- i
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	forgeCache := map[evidenceCacheKey][]model.Evidence{}
	progressEvery := progressInterval(len(refs))
	processed := 0

	// processOne は Discover 順に揃った 1 件を処理する。SQLite への書き込みは
	// すべてこの関数（＝このループを実行している goroutine）の中でのみ行う。
	processOne := func(o parseOutcome) error {
		ref := o.ref

		switch {
		case o.err != nil:
			result.ParseFailed++
			result.ParseFailures = append(result.ParseFailures, fmt.Sprintf("%s: %v", ref.Path, o.err))
			slog.Warn("ingest: パースに失敗しました", "path", ref.Path, "error", o.err)
			return nil
		case !o.needsIngest:
			result.SkippedUpToDate++
			return nil
		}

		sess := o.sess
		if isUnderJudgeWorkspace(sess.ProjectPath, judgeWorkspace) {
			result.SkippedJudgeWorkspace++
			return nil
		}
		if cfg.ExcludesProject(sess.ProjectPath) {
			result.SkippedExcludedProject++
			return nil
		}
		if excludesEntrypoint(cfg, sess.Entrypoint) {
			result.SkippedExcludedEntrypoint++
			return nil
		}

		costs := make([]store.UsageCost, 0, len(sess.Messages))
		for _, m := range sess.Messages {
			if m.Usage == nil {
				continue
			}
			usd, known := priceTable.Cost(m.Model, *m.Usage)
			costs = append(costs, store.UsageCost{Seq: m.Seq, CostUSD: usd, Known: known})
			if known {
				result.EstimatedCostUSD += usd
			} else {
				result.UnknownModels = appendUnique(result.UnknownModels, m.Model)
			}
			result.TotalInputTokens += m.Usage.InputTokens
			result.TotalOutputTokens += m.Usage.OutputTokens
			result.TotalCacheReadTokens += m.Usage.CacheRead
			result.TotalCacheCreationTokens += m.Usage.CacheCreation5m + m.Usage.CacheCreation1h
		}

		if !opts.DryRun {
			if err := db.SaveSession(sess, costs); err != nil {
				return fmt.Errorf("セッションの保存に失敗しました (%s): %w", sess.SessionID, err)
			}
			if err := db.MarkIngested(ref.Source, ref.Path, ref.ModTime, ref.Size, sess.ContentHash); err != nil {
				return fmt.Errorf("取り込み状態の記録に失敗しました (%s): %w", ref.Path, err)
			}
		}
		result.Ingested++

		if gitCollector != nil && !sess.IsSidechain {
			collectEvidenceForSession(ctx, gitCollector, forgeCollector, forgeCache, sess, db, result)
		}

		return nil
	}

	pending := make(map[int]parseOutcome, len(refs))
	nextIndex := 0
	interrupted := false

resultsLoop:
	for o := range resultsCh {
		pending[o.index] = o
		for {
			next, ok := pending[nextIndex]
			if !ok {
				break
			}
			delete(pending, nextIndex)
			nextIndex++
			processed++
			if progressEvery > 0 && processed%progressEvery == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "insights ingest: %d/%d 件処理済み\n", processed, len(refs))
			}

			if errors.Is(next.err, context.Canceled) {
				interrupted = true
				break resultsLoop
			}

			if err := processOne(next); err != nil {
				return nil, err
			}

			if ctx.Err() != nil {
				interrupted = true
				break resultsLoop
			}
		}
	}

	result.Interrupted = interrupted
	result.DurationSeconds = time.Since(start).Seconds()

	fmt.Fprintf(cmd.ErrOrStderr(), "insights ingest: 完了（%d/%d 件処理, 所要時間 %s）\n",
		processed, len(refs), time.Since(start).Round(10*time.Millisecond))

	if interrupted {
		return result, fmt.Errorf("ingest が中断されました（%d 件はここまでに保存済みです）: %w", result.Ingested, context.Canceled)
	}
	return result, nil
}

// resolveSince は取り込みの基準時刻を決める。
func resolveSince(db *store.DB, opts ingestOptions) (since time.Time, mode, label string, err error) {
	switch {
	case opts.All:
		return time.Time{}, "all", "", nil
	case !opts.Since.IsZero():
		return opts.Since, "since", opts.Since.Format(sinceDateLayout), nil
	default:
		t, err := db.LastIngestAt()
		if err != nil {
			return time.Time{}, "", "", fmt.Errorf("前回取り込み時刻の取得に失敗しました: %w", err)
		}
		if t.IsZero() {
			return time.Time{}, "incremental", "", nil
		}
		return t, "incremental", t.UTC().Format(time.RFC3339), nil
	}
}

// buildSources は cfg で有効なソースを名前引きできる形で組み立てる。
// 第 2 戻り値は「設定では有効だが、ログ置き場が無くて飛ばしたソース」の説明。
//
// ログ置き場の有無をここで見るのは、対応エージェントが複数あるためである。
// Claude Code しか使っていない環境でも codex は既定で有効なので、置き場が無い
// ソースで全体を失敗させると、ほとんどの利用者が ingest できなくなる。
// 逆に「全部見つからない」なら設定か環境がおかしいので、そのときだけ失敗させる。
func buildSources(cfg *config.Config) (map[string]source.Source, []string, error) {
	var candidates []source.Source

	if cfg.Sources.ClaudeCode.Enabled {
		root, err := config.ExpandPath(cfg.Sources.ClaudeCode.Root)
		if err != nil {
			return nil, nil, fmt.Errorf("claude-code のログ置き場パスの解決に失敗しました: %w", err)
		}
		candidates = append(candidates, claudecode.New(root))
	}
	if cfg.Sources.Codex.Enabled {
		root, err := config.ExpandPath(cfg.Sources.Codex.Root)
		if err != nil {
			return nil, nil, fmt.Errorf("codex のログ置き場パスの解決に失敗しました: %w", err)
		}
		candidates = append(candidates, codex.New(root))
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("有効なログソースがありません（sources.* がすべて無効です）")
	}

	out := map[string]source.Source{}
	var skipped []string
	for _, s := range candidates {
		if err := s.Available(); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", s.Name(), err))
			continue
		}
		out[s.Name()] = s
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("有効なログソースのログ置き場がどれも見つかりません: %s", strings.Join(skipped, ", "))
	}
	return out, skipped, nil
}

// discoverRefs は sources 全てをそれぞれの since 以降で Discover し、結果を結合する。
// ソース名でソートしてから走査するため、複数ソースがある場合でも結果の並びは決定的。
func discoverRefs(sources map[string]source.Source, sinceBySource map[string]time.Time) ([]source.Ref, error) {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)

	var all []source.Ref
	for _, name := range names {
		refs, err := sources[name].Discover(sinceBySource[name])
		if err != nil {
			return nil, fmt.Errorf("%s のログ発見に失敗しました: %w", name, err)
		}
		all = append(all, refs...)
	}
	return all, nil
}

// sourceSinceMap は Discover に渡す基準時刻をソースごとに決める。
//
// resolveSince が返す since は全ソース共通の「最後に何かを取り込んだ時刻」で、
// mode == "incremental" のとき使われる。これをそのまま新しく有効化したソースにも
// 適用すると、そのソースの過去ログは「基準時刻より前」として Discover の時点で
// 黙って除外される（除外プロジェクトやパース失敗と違い件数にも出ない）。
// ingest_state に一度もそのソースの記録が無ければ、基準時刻をゼロ値に戻して
// 初回は必ず全件を対象にする。
func sourceSinceMap(db *store.DB, sources map[string]source.Source, since time.Time, mode string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(sources))
	for name := range sources {
		s := since
		if mode == "incremental" && !s.IsZero() {
			has, err := db.HasIngestedSource(name)
			if err != nil {
				return nil, err
			}
			if !has {
				s = time.Time{}
			}
		}
		out[name] = s
	}
	return out, nil
}

// appendUnique は values に s が無ければ追加する（未知モデルの重複警告を防ぐ）。
func appendUnique(values []string, s string) []string {
	for _, v := range values {
		if v == s {
			return values
		}
	}
	return append(values, s)
}

// convertPricingOverrides は config.ModelPrice（config パッケージの型）を
// pricing.Rate（pricing パッケージの型）に変換する。両パッケージは互いに依存しない設計のため、
// この変換は呼び出し側（cli 層）の責務になる。
func convertPricingOverrides(overrides map[string]config.ModelPrice) map[string]pricing.Rate {
	if len(overrides) == 0 {
		return nil
	}
	out := make(map[string]pricing.Rate, len(overrides))
	for name, p := range overrides {
		out[name] = pricing.Rate{
			Input:        p.Input,
			Output:       p.Output,
			CacheWrite5m: p.CacheWrite5m,
			CacheWrite1h: p.CacheWrite1h,
			CacheRead:    p.CacheRead,
		}
	}
	return out
}

// workerCount はパース並行度を返す。CPU数と件数の小さい方（最低1）。
func workerCount(n int) int {
	if n <= 0 {
		return 1
	}
	w := runtime.NumCPU()
	if w > n {
		w = n
	}
	if w < 1 {
		w = 1
	}
	return w
}

// progressInterval は進捗表示の間隔（件数）を返す。おおよそ5%刻み。0 は「表示しない」。
func progressInterval(total int) int {
	if total <= 0 {
		return 0
	}
	step := total / 20
	if step < 1 {
		step = 1
	}
	return step
}

// --- 成果物収集 ---

// evidenceCacheKey は gh/glab（リモート API）呼び出しをメモ化するためのキー。
// リポジトリの単位としてセッションの ProjectPath をそのまま使う。同一 ingest 実行内で
// 同じプロジェクト・同じ日のセッションが複数あっても、このキーに対する収集は 1 回だけ行う。
type evidenceCacheKey struct {
	ProjectPath string
	Date        string // YYYY-MM-DD (UTC)
}

// gitOnlyEvidenceConfig は git commit のみを対象にした EvidenceConfig を作る
// （gh/glab を強制的に無効化する）。git commit の収集はローカルで完結し高速なため、
// セッションごとに呼んでもコスト・レート制限の問題が無い。
func gitOnlyEvidenceConfig(cfg config.EvidenceConfig) config.EvidenceConfig {
	c := cfg
	c.Gh = config.TristateFalse
	c.Glab = config.TristateFalse
	return c
}

// forgeOnlyEvidenceConfig は gh/glab（PR/Issue/MR）のみを対象にした EvidenceConfig を作る
// （git commit 収集を無効化する）。gh/glab はネットワーク越しの外部コマンドでレート制限の
// 対象にもなるため、(プロジェクトパス, 日付) 単位でメモ化して呼び出し回数を抑える。
func forgeOnlyEvidenceConfig(cfg config.EvidenceConfig) config.EvidenceConfig {
	c := cfg
	c.Git = false
	return c
}

// collectEvidenceForSession は 1 セッション分の成果物を収集して DB に保存する。失敗は
// ベストエフォートで、slog.Warn に記録するだけで ingest 自体は止めない。
func collectEvidenceForSession(
	ctx context.Context,
	gitCollector, forgeCollector *evidence.Collector,
	forgeCache map[evidenceCacheKey][]model.Evidence,
	sess *model.Session,
	db *store.DB,
	result *ingestResult,
) {
	if strings.TrimSpace(sess.ProjectPath) == "" || ctx.Err() != nil {
		return
	}

	var items []model.Evidence

	// git commit: セッションの実際の時間帯で、セッションごとに呼ぶ。
	gitQuery := evidence.Query{
		SessionID:   sess.SessionID,
		ProjectPath: sess.ProjectPath,
		GitBranch:   sess.GitBranch,
		From:        sess.StartedAt,
		To:          sess.EndedAt,
	}
	items = append(items, gitCollector.Collect(ctx, gitQuery)...)

	// gh/glab: (プロジェクトパス, 日付) 単位でメモ化する。同じ組み合わせを 2 回目以降は
	// キャッシュから使い回し、リモート API 呼び出しを増やさない。
	if !sess.StartedAt.IsZero() {
		day := sess.StartedAt.UTC().Format(sinceDateLayout)
		key := evidenceCacheKey{ProjectPath: sess.ProjectPath, Date: day}

		cached, ok := forgeCache[key]
		if !ok {
			dayStart := time.Date(sess.StartedAt.UTC().Year(), sess.StartedAt.UTC().Month(), sess.StartedAt.UTC().Day(), 0, 0, 0, 0, time.UTC)
			dayEnd := dayStart.Add(24*time.Hour - time.Nanosecond)
			forgeQuery := evidence.Query{
				SessionID:   sess.SessionID,
				ProjectPath: sess.ProjectPath,
				From:        dayStart,
				To:          dayEnd,
			}
			cached = forgeCollector.Collect(ctx, forgeQuery)
			forgeCache[key] = cached
		}
		for _, e := range cached {
			e.SessionID = sess.SessionID // キャッシュは代表セッションの ID を持つので、このセッション用に付け替える
			items = append(items, e)
		}
	}

	for _, e := range items {
		switch e.Kind {
		case "commit":
			result.Evidence.Commits++
		case "pr":
			result.Evidence.PullRequests++
		case "issue":
			result.Evidence.Issues++
		case "mr":
			result.Evidence.MergeRequests++
		}
	}

	if len(items) == 0 {
		return
	}
	if err := db.SaveEvidence(items); err != nil {
		slog.Warn("ingest: 成果物の保存に失敗しました", "session", sess.SessionID, "error", err)
	}
}

// --- 出力 ---

// renderIngestHuman は ingestResult を人間向けに整形して w に書き出す。
func renderIngestHuman(w io.Writer, r *ingestResult) error {
	fmt.Fprintln(w, "=== insights ingest ===")
	fmt.Fprintln(w)

	switch r.Mode {
	case "all":
		fmt.Fprintln(w, "対象: 全件")
	case "since":
		fmt.Fprintf(w, "対象: %s 以降\n", r.Since)
	default:
		if r.Since == "" {
			fmt.Fprintln(w, "対象: 全件（初回取り込み）")
		} else {
			fmt.Fprintf(w, "対象: 前回取り込み（%s）以降\n", r.Since)
		}
	}
	if r.DryRun {
		fmt.Fprintln(w, "モード: dry-run（DB へは書き込みません）")
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "発見: %d 件\n", r.Discovered)
	fmt.Fprintf(w, "取り込み: %d 件\n", r.Ingested)
	fmt.Fprintf(w, "スキップ（取り込み済み）: %d 件\n", r.SkippedUpToDate)
	fmt.Fprintf(w, "スキップ（除外プロジェクト）: %d 件\n", r.SkippedExcludedProject)
	fmt.Fprintf(w, "スキップ（除外 entrypoint）: %d 件\n", r.SkippedExcludedEntrypoint)
	fmt.Fprintf(w, "スキップ（評価ワークスペース）: %d 件\n", r.SkippedJudgeWorkspace)
	fmt.Fprintf(w, "パース失敗: %d 件\n", r.ParseFailed)
	for _, f := range r.ParseFailures {
		fmt.Fprintf(w, "  - %s\n", f)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "トークン集計（今回取り込み分）:")
	fmt.Fprintf(w, "  入力: %d\n", r.TotalInputTokens)
	fmt.Fprintf(w, "  出力: %d\n", r.TotalOutputTokens)
	fmt.Fprintf(w, "  キャッシュ読み取り: %d\n", r.TotalCacheReadTokens)
	fmt.Fprintf(w, "  キャッシュ作成: %d\n", r.TotalCacheCreationTokens)
	fmt.Fprintf(w, "推定コスト: $%.4f\n", r.EstimatedCostUSD)
	if len(r.UnknownModels) > 0 {
		fmt.Fprintln(w, "警告: 単価が未知のモデルがありました（上記の推定コストは過小評価です）:")
		for _, m := range r.UnknownModels {
			fmt.Fprintf(w, "  - %s\n", m)
		}
	}
	fmt.Fprintln(w)

	if r.Evidence.Enabled {
		fmt.Fprintln(w, "成果物収集:")
		if len(r.Evidence.AvailableMethods) == 0 {
			fmt.Fprintln(w, "  利用可能な手段: なし")
		} else {
			fmt.Fprintf(w, "  利用可能な手段: %s\n", strings.Join(r.Evidence.AvailableMethods, ", "))
		}
		fmt.Fprintf(w, "  commit: %d 件\n", r.Evidence.Commits)
		fmt.Fprintf(w, "  PR: %d 件\n", r.Evidence.PullRequests)
		fmt.Fprintf(w, "  Issue: %d 件\n", r.Evidence.Issues)
		fmt.Fprintf(w, "  MR: %d 件\n", r.Evidence.MergeRequests)
	} else if r.DryRun {
		fmt.Fprintln(w, "成果物収集: スキップしました (dry-run)")
	} else {
		fmt.Fprintln(w, "成果物収集: スキップしました (--no-evidence)")
	}
	fmt.Fprintln(w)

	if r.Interrupted {
		fmt.Fprintln(w, "警告: 中断されました。ここまでの分は DB に保存済みです。")
	}
	fmt.Fprintf(w, "所要時間: %.1fs\n", r.DurationSeconds)
	return nil
}
