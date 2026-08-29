package evidence

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/fuchigta/insights/internal/model"
)

// glabItem は glab の JSON 出力（GitLab API 由来の snake_case）の1件分。
// mr list / issue list に共通するフィールドのみ拾う。
type glabItem struct {
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
	WebURL      string    `json:"web_url"`
}

// collectGlab は glab CLI を使って MR と Issue の本文を集める。
// gh とはサブコマンド体系が全く異なるため、コマンドラインは共有しない。
//
// 注記: この環境には glab がインストールされておらず、実機での動作確認は
// できていない（glab が未検出のときに正しくスキップされることは確認済み）。
// サブコマンド名・JSON 出力フィールド名は GitLab CLI のドキュメント・GitLab
// API のレスポンス形状に基づく best-effort な実装。gh の `--search
// updated:A..B` に相当する日付範囲検索フラグが glab に確実に存在するかを
// この環境では検証できなかったため、取得後にクライアント側で UpdatedAt が
// [From, To] に収まるものだけへ絞り込む、より保守的な方式にしている。
func (c *Collector) collectGlab(ctx context.Context, q Query, repoSlug string) []model.Evidence {
	if c.glabPath == "" {
		slog.Warn("evidence: glab コマンドが見つからないためスキップします", "session", q.SessionID)
		return nil
	}
	var out []model.Evidence
	out = append(out, c.glabList(ctx, q, repoSlug, "mr", "mr")...)
	out = append(out, c.glabList(ctx, q, repoSlug, "issue", "issue")...)
	return out
}

// glabList は `glab <subcommand> list --repo <repo> --all --output json` を実行し、
// セッションの時間帯に更新された MR/Issue を model.Evidence へ変換する。
// 取得件数が上限を超える場合は先頭から切り詰め、slog.Warn で明示する。
func (c *Collector) glabList(ctx context.Context, q Query, repoSlug, subcommand, kind string) []model.Evidence {
	limit := c.maxItemsOrDefault()

	args := []string{subcommand, "list", "--repo", repoSlug, "--all", "--output", "json"}

	out, err := c.runGlab(ctx, args...)
	if err != nil {
		slog.Warn("evidence: glab の取得に失敗しました", "session", q.SessionID, "kind", kind, "repo", repoSlug, "error", err)
		return nil
	}

	var items []glabItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		slog.Warn("evidence: glab の出力を解釈できませんでした", "session", q.SessionID, "kind", kind, "repo", repoSlug, "error", err)
		return nil
	}

	var inRangeItems []glabItem
	for _, it := range items {
		if timeInRange(it.UpdatedAt, q.From, q.To) {
			inRangeItems = append(inRangeItems, it)
		}
	}

	if len(inRangeItems) > limit {
		slog.Warn("evidence: glab の取得件数が上限を超えたため打ち切りました",
			"session", q.SessionID, "kind", kind, "repo", repoSlug, "limit", limit)
		inRangeItems = inRangeItems[:limit]
	}

	result := make([]model.Evidence, 0, len(inRangeItems))
	for _, it := range inRangeItems {
		result = append(result, model.Evidence{
			SessionID: q.SessionID,
			Kind:      kind,
			Ref:       it.WebURL,
			Timestamp: it.UpdatedAt,
			Title:     it.Title,
			Body:      it.Description,
		})
	}
	return result
}

// timeInRange は t が [from, to] に収まるかを判定する。from/to がゼロ値なら
// その側の境界は無いものとして扱う。
func timeInRange(t, from, to time.Time) bool {
	if !from.IsZero() && t.Before(from) {
		return false
	}
	if !to.IsZero() && t.After(to) {
		return false
	}
	return true
}
