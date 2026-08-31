package cli

import (
	"strings"

	"github.com/fuchigta/insights/internal/config"
	"github.com/fuchigta/insights/internal/store"
)

// 除外設定（exclude.projects / exclude.entrypoints）は取り込み時のゲートとしてだけ
// 掛けると、「設定を足す前に取り込んだセッション」が DB に残り、評価もレポートも
// そのまま対象にしてしまう。除外に気づくのはたいてい一度取り込んだ後なので、
// それでは設定を足しても効かないのと変わらない。
//
// そこで取り込み時に加えて、DB から読んだ後にも同じ判定を掛ける。除外は
// 「この設定で見えるべきでないもの」の宣言として扱い、いつ足しても効くようにする。
// 既に入っているデータを消しはしない（除外を外せばまた見えるようにするため）。

// excludesEntrypoint は entrypoint が cfg.Exclude.Entrypoints に含まれるかを判定する。
func excludesEntrypoint(cfg *config.Config, entrypoint string) bool {
	if strings.TrimSpace(entrypoint) == "" {
		return false
	}
	for _, e := range cfg.Exclude.Entrypoints {
		if strings.EqualFold(strings.TrimSpace(e), entrypoint) {
			return true
		}
	}
	return false
}

// excludedSession は projectPath / entrypoint のどちらかが除外設定に該当するかを判定する。
func excludedSession(cfg *config.Config, projectPath, entrypoint string) bool {
	return cfg.ExcludesProject(projectPath) || excludesEntrypoint(cfg, entrypoint)
}

// filterExcludedSessions は DB から読んだセッションから除外対象を取り除き、
// 残りと除外件数を返す。呼び出し側は除外件数を結果に載せて、黙って減っていないことを
// 分かるようにする。
func filterExcludedSessions(cfg *config.Config, rows []store.SessionRow) (kept []store.SessionRow, excluded int) {
	kept = make([]store.SessionRow, 0, len(rows))
	for _, r := range rows {
		if excludedSession(cfg, r.ProjectPath, r.Entrypoint) {
			excluded++
			continue
		}
		kept = append(kept, r)
	}
	return kept, excluded
}
