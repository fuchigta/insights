package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/fuchigta/insights/internal/model"
)

// ghItem は `gh pr list` / `gh issue list --json number,title,body,updatedAt,url` の1件分。
type ghItem struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updatedAt"`
	URL       string    `json:"url"`
}

// collectGh は gh CLI を使って PR と Issue の本文を集める。
// glab とはサブコマンド体系が全く異なるため、コマンドラインは共有しない。
//
// host は origin のホスト名。GitHub Enterprise Server では github.com 以外に
// なるため、GH_HOST 環境変数で gh に見に行かせる先を明示する。
func (c *Collector) collectGh(ctx context.Context, q Query, host, repoSlug string) []model.Evidence {
	if c.ghPath == "" {
		slog.Warn("evidence: gh コマンドが見つからないためスキップします", "session", q.SessionID)
		return nil
	}
	var out []model.Evidence
	out = append(out, c.ghList(ctx, q, host, repoSlug, "pr", "pr")...)
	out = append(out, c.ghList(ctx, q, host, repoSlug, "issue", "issue")...)
	return out
}

// ghList は `gh <subcommand> list` を実行し、セッションの時間帯に更新された
// PR/Issue を model.Evidence へ変換する。
//
// 上限件数（maxItemsOrDefault）+1 件を要求し、実際にそれを超えて返ってきたら
// 「上限で打ち切った」ことが分かるので、超過分は切り捨てた上で slog.Warn に
// 明示する（黙って切り捨てない）。
func (c *Collector) ghList(ctx context.Context, q Query, host, repoSlug, subcommand, kind string) []model.Evidence {
	limit := c.maxItemsOrDefault()

	search := fmt.Sprintf("updated:%s..%s", ghSearchDate(q.From), ghSearchDate(q.To))
	args := []string{
		subcommand, "list",
		"--repo", repoSlug,
		"--state", "all",
		"--search", search,
		"--json", "number,title,body,updatedAt,url",
		"--limit", strconv.Itoa(limit + 1),
	}

	out, err := c.runGh(ctx, host, args...)
	if err != nil {
		slog.Warn("evidence: gh の取得に失敗しました", "session", q.SessionID, "kind", kind, "host", host, "repo", repoSlug, "error", err)
		return nil
	}

	var items []ghItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		slog.Warn("evidence: gh の出力を解釈できませんでした", "session", q.SessionID, "kind", kind, "repo", repoSlug, "error", err)
		return nil
	}

	if len(items) > limit {
		slog.Warn("evidence: gh の取得件数が上限を超えたため打ち切りました",
			"session", q.SessionID, "kind", kind, "repo", repoSlug, "limit", limit)
		items = items[:limit]
	}

	result := make([]model.Evidence, 0, len(items))
	for _, it := range items {
		result = append(result, model.Evidence{
			SessionID: q.SessionID,
			Kind:      kind,
			Ref:       it.URL,
			Timestamp: it.UpdatedAt,
			Title:     it.Title,
			Body:      it.Body,
		})
	}
	return result
}

// ghSearchDate は gh の --search クエリで使える日時表現（RFC3339, UTC）を返す。
func ghSearchDate(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
