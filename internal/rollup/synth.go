// Package rollup の AI 呼び出し部分。集計済みの Daily に対して、日報（Narrative）と
// 振り返り（Retro）を別々の AI 呼び出しで埋める。
package rollup

import (
	"context"
	_ "embed" // go:embed でプロンプトテンプレートを埋め込むために必要
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fuchigta/insights/internal/judge"
	"github.com/fuchigta/insights/internal/model"
)

//go:embed prompts/daily.md
var dailyPromptTemplate string

//go:embed prompts/retro.md
var retroPromptTemplate string

// PromptVersion は日報・振り返りプロンプト（prompts/daily.md, prompts/retro.md）のバージョン。
//
// 規約: これらのプロンプトの内容を変更したら、必ずこの値も変更すること。
// 生成物の解釈（JSON スキーマ・観点）が変わった過去の Daily と新しいプロンプトの結果を
// 混同しないようにするための目印であり、呼び出し側が Meta やキャッシュキーに使うことを想定する。
const PromptVersion = "rollup-synth-v6"

// dailySchema は日報生成に期待する出力の JSON Schema。
// internal/judge のバックエンドが `claude -p --json-schema` にそのまま渡せるよう、
// type / properties / required / additionalProperties: false を明示した正式な JSON Schema として書く
// （プロンプト側の「JSON のみを出力せよ」という指示は保険であり、検証の本体はこちらのスキーマ）。
var dailySchema = json.RawMessage(`{
	"title": "DailyNarrative",
	"description": "internal/rollup.Narrative と 1:1 対応する、日報生成の出力。",
	"type": "object",
	"required": ["headline", "body", "highlights"],
	"additionalProperties": false,
	"properties": {
		"headline": {
			"type": "string",
			"description": "今日 1 日を一言で表す見出し（1 行）。"
		},
		"body": {
			"type": "string",
			"description": "Markdown 形式の本文。プロジェクトをまたいだ活動を、時系列またはプロジェクト単位でまとめる。ユーザーがやったことと AI がやったことを読み分けられるように書く。"
		},
		"highlights": {
			"type": "array",
			"description": "箇条書きの成果。特筆すべき成果が無ければ空配列でよい。",
			"items": {"type": "string"}
		}
	}
}`)

// retroSchema は振り返り生成に期待する出力の JSON Schema。
// dailySchema と同様、`claude -p --json-schema` にそのまま渡せる正式な JSON Schema として書く。
var retroSchema = json.RawMessage(`{
	"title": "Retro",
	"description": "internal/rollup.Retro と 1:1 対応する、振り返り生成の出力。",
	"type": "object",
	"required": ["headline", "verdict", "body", "cost_observation", "proposals", "verifications", "outliers", "project_reviews"],
	"additionalProperties": false,
	"properties": {
		"headline": {
			"type": "string",
			"description": "「今日投じたコストに見合った価値が出たか」への 1 行の答え。レポート冒頭に出す結論であり、分布の要約ではない。"
		},
		"verdict": {
			"type": "string",
			"enum": ["worth_it", "mixed", "not_worth_it", "insufficient_data"],
			"description": "headline の結論を表す区分。判断材料が乏しい日は insufficient_data。"
		},
		"body": {
			"type": "string",
			"description": "Markdown 形式の本文。プロジェクト個別の当たり外れは project_reviews に譲り、任せ方・モデル選択と委譲・検収の傾向など日を横断する所見のみを書く。ユーザーの行動と AI の行動を区別し、対話実行と非対話実行（自動実行）も混ぜずに書く。"
		},
		"cost_observation": {
			"type": "string",
			"description": "コストの所見。論点は「作業の中身に対して妥当なモデル・effort を選べていたか、高いモデルが抱えた単純作業を安いモデルへ委譲できなかったか」だけ。キャッシュの効き具合やトークン量には触れない（長いセッションでは自然に大きくなる値で、やり方の良し悪しを表さないため）。"
		},
		"proposals": {
			"type": "array",
			"description": "新しい改善提案（3件程度）。",
			"items": {
				"type": "object",
				"required": ["title", "detail", "category"],
				"additionalProperties": false,
				"properties": {
					"title": {"type": "string"},
					"detail": {
						"type": "string",
						"description": "後から実行有無を客観的に判定できる具体的な内容。ユーザーの任せ方（依頼の仕方・作業の分け方・モデルと effort の選び方・委譲の仕方・検収の仕方）として書くこと。非対話実行（execution_mode が automated）については、起動時のプロンプトか、そこから呼び出しているスキル／スラッシュコマンドの定義に何を書き足すかとして書くこと（実行中の介入や検収を求める提案は実行不可能なので書かない）。「もっと丁寧にやる」のような検証不能な精神論と、insights 自体への機能要望は禁止。"
					},
					"category": {"type": "string"}
				}
			}
		},
		"verifications": {
			"type": "array",
			"description": "前回までの未決着提案の検証結果。",
			"items": {
				"type": "object",
				"required": ["action_id", "title", "status", "verdict"],
				"additionalProperties": false,
				"properties": {
					"action_id": {"type": "integer"},
					"title": {"type": "string"},
					"status": {"type": "string", "enum": ["open", "done", "dropped", "expired"]},
					"verdict": {"type": "string"}
				}
			}
		},
		"outliers": {
			"type": "array",
			"description": "コスト対価値が外れているセッション。該当が無ければ空配列。",
			"items": {
				"type": "object",
				"required": ["session_id", "cost_usd", "reason"],
				"additionalProperties": false,
				"properties": {
					"session_id": {"type": "string"},
					"cost_usd": {"type": "number"},
					"reason": {"type": "string"}
				}
			}
		},
		"project_reviews": {
			"type": "object",
			"description": "プロジェクト単位の振り返り。キーは project_path。個別に語る価値があるプロジェクトのみ含めればよい。",
			"additionalProperties": {
				"type": "object",
				"required": ["verdict", "summary", "improvement"],
				"additionalProperties": false,
				"properties": {
					"verdict": {
						"type": "string",
						"enum": ["worth_it", "mixed", "not_worth_it", "insufficient_data"]
					},
					"summary": {
						"type": "string",
						"description": "定量（コスト・時間・達成率）と定性（何が起きたか）の両面から、コストに見合った価値が出たかを具体的なセッションを挙げて書く。分布の数字を読み上げるだけの文章は禁止。"
					},
					"improvement": {
						"type": "string",
						"description": "このプロジェクトで次に変えること。ユーザーの任せ方として書く。無ければ空文字。"
					}
				}
			}
		}
	}
}`)

// SynthInput は Synthesize の入力。
type SynthInput struct {
	GlobalGoal  string
	OpenActions []model.Action // 未決着の過去提案。検証対象
	Model       string
	RecentDays  []*Daily // 直近数日分（傾向の材料）。nil 可
}

// dailyContext は日報生成 AI に渡す JSON コンテキスト。
type dailyContext struct {
	Date       string           `json:"date"`
	GlobalGoal string           `json:"global_goal,omitempty"`
	Totals     Totals           `json:"totals"`
	ByModel    []ModelUsage     `json:"by_model"`
	ByProject  []projectContext `json:"by_project"`
	Sessions   []SessionCard    `json:"sessions"`
	Facets     Facets           `json:"facets"`
}

// retroContext は振り返り生成 AI に渡す JSON コンテキスト。
// Totals と ByProject（プロジェクト単位の facets /
// achieved_ratio / cost_share / highlights / rolled_up を含む）をそのまま渡すことで、
// プロジェクト単位の振り返り（project_reviews）を書くための材料にする。
type retroContext struct {
	Date        string             `json:"date"`
	GlobalGoal  string             `json:"global_goal,omitempty"`
	Totals      Totals             `json:"totals"`
	ByModel     []ModelUsage       `json:"by_model"`
	ByProject   []projectContext   `json:"by_project"`
	Sessions    []SessionCard      `json:"sessions"`
	Facets      Facets             `json:"facets"`
	RecentDays  []recentDaySummary `json:"recent_days,omitempty"`
	OpenActions []openActionRef    `json:"open_actions,omitempty"`
}

// projectContext は AI に渡すプロジェクト単位の集計。ProjectStat から Review
// （AI がこの呼び出しで埋める出力専用フィールド）を除いたもの。
// 呼び出し前は Review がゼロ値なので、そのまま渡すと「空の review」が入力に混ざって
// 紛らわしくなるため、入力用に絞った型を別に持つ。
type projectContext struct {
	ProjectPath       string        `json:"project_path"`
	ProjectLabel      string        `json:"project_label"`
	Sessions          int           `json:"sessions"`
	DurationMinutes   float64       `json:"duration_minutes"`
	CostUSD           float64       `json:"cost_usd"`
	CostShare         float64       `json:"cost_share"`
	Goal              string        `json:"goal,omitempty"`
	EvaluatedSessions int           `json:"evaluated_sessions"`
	Facets            Facets        `json:"facets"`
	AchievedRatio     float64       `json:"achieved_ratio"`
	Highlights        []SessionCard `json:"highlights"`
	RolledUp          RolledUpGroup `json:"rolled_up"`
}

// toProjectContext は ProjectStat の列を AI 入力用の projectContext の列に変換する。
func toProjectContext(stats []ProjectStat) []projectContext {
	out := make([]projectContext, 0, len(stats))
	for _, p := range stats {
		out = append(out, projectContext{
			ProjectPath:       p.ProjectPath,
			ProjectLabel:      p.ProjectLabel,
			Sessions:          p.Sessions,
			DurationMinutes:   p.DurationMinutes,
			CostUSD:           p.CostUSD,
			CostShare:         p.CostShare,
			Goal:              p.Goal,
			EvaluatedSessions: p.EvaluatedSessions,
			Facets:            p.Facets,
			AchievedRatio:     p.AchievedRatio,
			Highlights:        p.Highlights,
			RolledUp:          p.RolledUp,
		})
	}
	return out
}

// recentDaySummary は直近日の傾向を見るための軽量サマリ。
type recentDaySummary struct {
	Date           string  `json:"date"`
	Sessions       int     `json:"sessions"`
	CostUSD        float64 `json:"cost_usd"`
	EvaluatedCount int     `json:"evaluated_count"`
	AchievedCount  int     `json:"achieved_count"`
}

// openActionRef は検証対象の過去提案を AI に渡すための最小表現。
type openActionRef struct {
	ActionID  int64  `json:"action_id"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Category  string `json:"category"`
	CreatedOn string `json:"created_on"`
}

// Synthesize は集計済みの Daily に Narrative と Retro を埋める。
// 日報と振り返りは性質が違う（記録 vs 分析）ため、別々の AI 呼び出しとして実行する。
//
// その日にセッションが 1 件も無ければ、AI は一切呼び出さない（呼び出す材料が無いため）。
// 日報・振り返りいずれかの呼び出しが失敗しても、もう片方の結果は d に反映されたまま残る。
// 失敗があった場合は、どちらの呼び出しがなぜ失敗したかを表すエラーを返す
// （呼び出し側はこれをログや Meta 相当の記録に転記できる）。
func Synthesize(ctx context.Context, j judge.Judge, d *Daily, in SynthInput) error {
	if d == nil {
		return fmt.Errorf("Synthesize: daily が nil")
	}
	if len(d.Sessions) == 0 {
		// その日にセッションが無ければ AI を呼ばない。
		return nil
	}
	if j == nil {
		return fmt.Errorf("Synthesize: judge が nil")
	}

	var errs []error

	if err := synthesizeDaily(ctx, j, d, in); err != nil {
		errs = append(errs, fmt.Errorf("日報生成に失敗: %w", err))
	}
	if err := synthesizeRetro(ctx, j, d, in); err != nil {
		errs = append(errs, fmt.Errorf("振り返り生成に失敗: %w", err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// synthesizeDaily は日報生成の 1 回の AI 呼び出しを行い、成功した場合のみ d.Narrative を上書きする。
func synthesizeDaily(ctx context.Context, j judge.Judge, d *Daily, in SynthInput) error {
	ctxData := dailyContext{
		Date:       d.Date,
		GlobalGoal: in.GlobalGoal,
		Totals:     d.Totals,
		ByModel:    d.ByModel,
		ByProject:  toProjectContext(d.ByProject),
		Sessions:   d.Sessions,
		Facets:     d.Facets,
	}
	prompt, err := json.MarshalIndent(ctxData, "", "  ")
	if err != nil {
		return fmt.Errorf("日報コンテキストのシリアライズに失敗: %w", err)
	}

	raw, err := j.Evaluate(ctx, judge.Request{
		System: dailyPromptTemplate,
		Prompt: string(prompt),
		Schema: dailySchema,
		Model:  in.Model,
	})
	if err != nil {
		return fmt.Errorf("judge 呼び出しに失敗: %w", err)
	}

	var nar Narrative
	if err := json.Unmarshal(raw, &nar); err != nil {
		return fmt.Errorf("日報 JSON のパースに失敗: %w", err)
	}

	d.Narrative = nar
	return nil
}

// synthesizeRetro は振り返り生成の 1 回の AI 呼び出しを行い、成功した場合のみ d.Retro を上書きする。
func synthesizeRetro(ctx context.Context, j judge.Judge, d *Daily, in SynthInput) error {
	ctxData := retroContext{
		Date:       d.Date,
		GlobalGoal: in.GlobalGoal,
		Totals:     d.Totals,
		ByModel:    d.ByModel,
		ByProject:  toProjectContext(d.ByProject),
		Sessions:   d.Sessions,
		Facets:     d.Facets,
	}
	for _, rd := range in.RecentDays {
		if rd == nil {
			continue
		}
		ctxData.RecentDays = append(ctxData.RecentDays, recentDaySummary{
			Date:           rd.Date,
			Sessions:       rd.Totals.Sessions,
			CostUSD:        rd.Totals.CostUSD,
			EvaluatedCount: sumFacet(rd.Facets.Outcome),
			AchievedCount:  rd.Facets.Outcome["achieved"],
		})
	}
	for _, a := range in.OpenActions {
		ctxData.OpenActions = append(ctxData.OpenActions, openActionRef{
			ActionID:  a.ID,
			Title:     a.Title,
			Detail:    a.Detail,
			Category:  a.Category,
			CreatedOn: a.CreatedOn,
		})
	}

	prompt, err := json.MarshalIndent(ctxData, "", "  ")
	if err != nil {
		return fmt.Errorf("振り返りコンテキストのシリアライズに失敗: %w", err)
	}

	raw, err := j.Evaluate(ctx, judge.Request{
		System: retroPromptTemplate,
		Prompt: string(prompt),
		Schema: retroSchema,
		Model:  in.Model,
	})
	if err != nil {
		return fmt.Errorf("judge 呼び出しに失敗: %w", err)
	}

	var retro Retro
	if err := json.Unmarshal(raw, &retro); err != nil {
		return fmt.Errorf("振り返り JSON のパースに失敗: %w", err)
	}

	applyProjectReviews(d, retro.ProjectReviews)
	d.Retro = retro
	return nil
}

// applyProjectReviews は Retro.ProjectReviews（キー: project_path）を対応する
// d.ByProject[i].Review へ反映する。一致しない project_path はレポートを壊さないよう
// 無視するが、プロンプトとデータの不整合（AI が存在しないプロジェクトを挙げた等）に
// 気付けるよう警告ログを出す。
func applyProjectReviews(d *Daily, reviews map[string]ProjectReview) {
	if len(reviews) == 0 {
		return
	}
	matched := make(map[string]bool, len(reviews))
	for i := range d.ByProject {
		key := d.ByProject[i].ProjectPath
		if rv, ok := reviews[key]; ok {
			d.ByProject[i].Review = rv
			matched[key] = true
		}
	}
	for key := range reviews {
		if !matched[key] {
			slog.Warn("rollup: 振り返りの project_reviews が既知の project_path と一致しません（無視します）",
				"date", d.Date, "project_path", key)
		}
	}
}

// sumFacet は 1 つの facet マップの合計値（= 評価済みセッション数）を返す。
func sumFacet(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}
