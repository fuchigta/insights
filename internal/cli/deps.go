package cli

import (
	"fmt"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/judge/claudecli"
	"github.com/fuchigta/insights/internal/judge/codexcli"
	"github.com/fuchigta/insights/internal/pricing"
	"github.com/fuchigta/insights/internal/store"
)

// このファイルは複数のサブコマンドが共有する依存の組み立てをまとめる。
// 同じ初期化を各コマンドに書き散らすと、設定の解釈がコマンドごとにずれていく。

// openStore は設定（--db の上書き適用済み）から DB を開く。
// 呼び出し側が Close する責任を持つ。
func openStore(cfg *config.Config) (*store.DB, error) {
	dbPath, err := config.ExpandPath(cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("db パスの解決に失敗しました: %w", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("DB のオープンに失敗しました (%s): %w", dbPath, err)
	}
	return db, nil
}

// buildPriceTable は埋め込み単価表に設定の overrides をマージして返す。
func buildPriceTable(cfg *config.Config) (*pricing.Table, error) {
	t, err := pricing.Load(convertPricingOverrides(cfg.Pricing.Overrides))
	if err != nil {
		return nil, fmt.Errorf("単価表の読み込みに失敗しました: %w", err)
	}
	return t, nil
}

// newJudge は設定から評価バックエンドを組み立てる関数。実体は buildJudge で、
// 統合テストがフェイク実装に差し替えるための唯一の穴として変数にしている
// （コマンドを通した検証で claude を実際に呼ばないため。差し替えないかぎり挙動は同じ）。
var newJudge = buildJudge

// バックエンド識別子。config の judge.backend に書く値であり、
// 各実装の Name() とも一致させる。
const (
	judgeBackendClaudeCLI = "claude-cli"
	judgeBackendCodexCLI  = "codex-cli"
)

// buildJudge は設定から AI 評価バックエンドを組み立てる。
//
// 戻り値をインターフェースにしているのは、上記の差し替えを可能にするため。
// 評価コストの記録に使う EvaluateRun は型アサーションで拾う（evaluateWithRunInfo 参照）。
func buildJudge(cfg *config.Config) (judge.Judge, error) {
	switch cfg.Judge.Backend {
	case "", judgeBackendClaudeCLI:
		j := claudecli.New(claudecli.Options{
			Model:   cfg.Judge.Model,
			Timeout: cfg.Judge.Timeout.Duration,
		})
		if err := j.Available(); err != nil {
			return nil, fmt.Errorf("評価バックエンド claude-cli が利用できません: %w", err)
		}
		return j, nil
	case judgeBackendCodexCLI:
		j := codexcli.New(codexcli.Options{
			Model:   cfg.Judge.Model,
			Timeout: cfg.Judge.Timeout.Duration,
		})
		if err := j.Available(); err != nil {
			return nil, fmt.Errorf("評価バックエンド codex-cli が利用できません: %w", err)
		}
		return j, nil
	default:
		return nil, fmt.Errorf("未知の評価バックエンドです: %q (対応: %s, %s)",
			cfg.Judge.Backend, judgeBackendClaudeCLI, judgeBackendCodexCLI)
	}
}

// judgeReportsCost はそのバックエンドが 1 回の実行に掛かった費用（USD）を
// 報告するかどうかを返す。
//
// claude -p は total_cost_usd を返すが、codex exec は返さない（ChatGPT プランでの
// 利用が主で、CLI 側が USD 建ての実費を知らないため）。報告しないバックエンドで
// 「推定コスト $0.0000」とだけ出すと、無料であるかのように読めてしまう。
// 事前確認の文面を変えるために、ここで一元的に判定する。
func judgeReportsCost(backend string) bool {
	switch backend {
	case "", judgeBackendClaudeCLI:
		return true
	default:
		return false
	}
}
