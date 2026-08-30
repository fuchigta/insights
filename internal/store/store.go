// Package store は insights の永続化層。modernc.org/sqlite（CGo 不要）を database/sql 経由で使い、
// internal/model の正規化データを SQLite に出し入れする。
//
// 依存の向きは model -> store の一方向のみで、store は internal/pricing に依存しない。
// コスト算出は呼び出し側が pricing パッケージで行い、結果を UsageCost として渡す。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/fuchigta/insights/internal/model"
)

// timeLayout は保存に使う時刻表現。必ず UTC の RFC3339。
const timeLayout = time.RFC3339

// DB は SQLite 上の永続化層への接続。
type DB struct {
	db *sql.DB
}

// Open は path の SQLite ファイルを開く。親ディレクトリが無ければ作成し、
// 未適用のマイグレーションを適用したうえで、WAL・外部キー制約・busy_timeout を設定する。
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("db ディレクトリ %q の作成に失敗: %w", dir, err)
		}
	}

	// modernc.org/sqlite の DSN ショートハンドキーで journal_mode/foreign_keys/busy_timeout を設定する。
	// https://pkg.go.dev/modernc.org/sqlite#Driver.Open のクエリパラメータ仕様に準拠。
	dsn := path + "?_busy_timeout=5000&_foreign_keys=1&_journal_mode=WAL"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db %q のオープンに失敗: %w", path, err)
	}

	// SQLite は同時書き込みをサポートせず、modernc.org/sqlite の各コネクションは
	// 独立した SQLite ハンドルになるため、プールを1本に絞って直列化し
	// "database is locked" を避ける。CLI ツールとしての用途では十分な性能。
	sqlDB.SetMaxOpenConns(1)

	d := &DB{db: sqlDB}
	if err := d.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// Close は DB 接続を閉じる。
func (d *DB) Close() error {
	if err := d.db.Close(); err != nil {
		return fmt.Errorf("db クローズに失敗: %w", err)
	}
	return nil
}

// migrate は schema_migrations に記録された適用済みバージョンと migrations を突き合わせ、
// 未適用のものだけを 1 件ずつトランザクションで適用する。
// 同じバージョンが二重に適用されることはない（Open を何度呼んでも冪等）。
func (d *DB) migrate() error {
	if _, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("schema_migrations の作成に失敗: %w", err)
	}

	applied := map[int]bool{}
	rows, err := d.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("適用済みマイグレーションの取得に失敗: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("マイグレーションバージョンの読み取りに失敗: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("マイグレーション一覧の走査に失敗: %w", err)
	}
	rows.Close()

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}

		tx, err := d.db.Begin()
		if err != nil {
			return fmt.Errorf("マイグレーション v%d の開始に失敗: %w", m.version, err)
		}
		if _, err := tx.Exec(m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("マイグレーション v%d の適用に失敗: %w", m.version, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(timeLayout),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("マイグレーション v%d の記録に失敗: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("マイグレーション v%d のコミットに失敗: %w", m.version, err)
		}
	}
	return nil
}

// --- 時刻変換ヘルパー ---

// toUTCString は time.Time を UTC の RFC3339 文字列に変換する。ゼロ値は空文字列。
func toUTCString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

// parseUTCString は RFC3339 文字列を UTC の time.Time に戻す。空文字列はゼロ値。
func parseUTCString(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("時刻 %q のパースに失敗: %w", s, err)
	}
	return t.UTC(), nil
}

// boolToInt は SQLite の INTEGER 列へ真偽値を保存するための変換。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- セッション取り込み ---

// SaveSession はセッション本体・メッセージ・使用量イベントを 1 トランザクションで保存する。
//
// 冪等性の担保方法:
//   - sessions は session_id を主キーとし、ON CONFLICT DO UPDATE で上書きする
//     （行が増えるのではなく既存行が最新内容に置き換わる）。
//   - messages / usage_events は session_id に紐づく既存行を先に全削除してから、
//     Session.Messages の内容で作り直す（delete-then-insert）。
//
// そのため同じ Session を同じ costs で 2 回渡しても、各テーブルの行数・内容は変わらない。
//
// costs は model.Message.Seq をキーにした算出済みコスト。store は internal/pricing に
// 依存しないため、コストは呼び出し側で算出して渡す。costs に対応する seq が無い、または
// Known=false の usage イベントは cost_usd=0, cost_known=0 として保存する。
func (d *DB) SaveSession(s *model.Session, costs []UsageCost) error {
	if s == nil {
		return fmt.Errorf("SaveSession: session が nil")
	}

	costBySeq := make(map[int]UsageCost, len(costs))
	for _, c := range costs {
		costBySeq[c.Seq] = c
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("トランザクション開始に失敗: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // Commit 済みなら no-op

	userCount, assistantCount, toolErrorCount := 0, 0, 0
	for _, m := range s.Messages {
		switch m.Role {
		case model.RoleUser:
			userCount++
		case model.RoleAssistant:
			assistantCount++
		}
		if m.IsError {
			toolErrorCount++
		}
	}

	ingestedAt := time.Now().UTC().Format(timeLayout)

	if _, err := tx.Exec(`
		INSERT INTO sessions (
			session_id, source, project_path, project_label, git_branch, entrypoint,
			is_sidechain, parent_session_id, started_at, ended_at, first_prompt, title,
			transcript_path, content_hash, message_count, user_message_count,
			assistant_message_count, tool_error_count, ingested_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			source                   = excluded.source,
			project_path             = excluded.project_path,
			project_label            = excluded.project_label,
			git_branch               = excluded.git_branch,
			entrypoint               = excluded.entrypoint,
			is_sidechain             = excluded.is_sidechain,
			parent_session_id        = excluded.parent_session_id,
			started_at               = excluded.started_at,
			ended_at                 = excluded.ended_at,
			first_prompt             = excluded.first_prompt,
			title                    = excluded.title,
			transcript_path          = excluded.transcript_path,
			content_hash             = excluded.content_hash,
			message_count            = excluded.message_count,
			user_message_count       = excluded.user_message_count,
			assistant_message_count  = excluded.assistant_message_count,
			tool_error_count         = excluded.tool_error_count,
			ingested_at              = excluded.ingested_at
	`,
		s.SessionID, s.Source, s.ProjectPath, s.ProjectLabel, s.GitBranch, s.Entrypoint,
		boolToInt(s.IsSidechain), s.ParentSessionID, toUTCString(s.StartedAt), toUTCString(s.EndedAt),
		s.FirstPrompt, s.Title, s.TranscriptPath, s.ContentHash, len(s.Messages), userCount,
		assistantCount, toolErrorCount, ingestedAt,
	); err != nil {
		return fmt.Errorf("sessions(%s) の保存に失敗: %w", s.SessionID, err)
	}

	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, s.SessionID); err != nil {
		return fmt.Errorf("messages(%s) の削除に失敗: %w", s.SessionID, err)
	}
	if _, err := tx.Exec(`DELETE FROM usage_events WHERE session_id = ?`, s.SessionID); err != nil {
		return fmt.Errorf("usage_events(%s) の削除に失敗: %w", s.SessionID, err)
	}

	msgStmt, err := tx.Prepare(`
		INSERT INTO messages (
			session_id, seq, ts, role, model, effort, text, truncated, tool_name, is_error, is_meta
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("messages の準備に失敗: %w", err)
	}
	defer msgStmt.Close()

	usageStmt, err := tx.Prepare(`
		INSERT INTO usage_events (
			session_id, seq, ts, model, input_tokens, output_tokens, thinking_tokens,
			cache_creation_5m, cache_creation_1h, cache_read, service_tier, cost_usd, cost_known
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("usage_events の準備に失敗: %w", err)
	}
	defer usageStmt.Close()

	for _, m := range s.Messages {
		if _, err := msgStmt.Exec(
			s.SessionID, m.Seq, toUTCString(m.Timestamp), string(m.Role), m.Model, m.Effort,
			m.Text, boolToInt(m.Truncated), m.ToolName, boolToInt(m.IsError), boolToInt(m.IsMeta),
		); err != nil {
			return fmt.Errorf("messages(%s, seq=%d) の保存に失敗: %w", s.SessionID, m.Seq, err)
		}

		if m.Usage == nil {
			continue
		}
		c := costBySeq[m.Seq] // 見つからなければゼロ値（CostUSD=0, Known=false）でよい
		costUSD := c.CostUSD
		if !c.Known {
			costUSD = 0
		}
		if _, err := usageStmt.Exec(
			s.SessionID, m.Seq, toUTCString(m.Timestamp), m.Model,
			m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.ThinkingTokens,
			m.Usage.CacheCreation5m, m.Usage.CacheCreation1h, m.Usage.CacheRead,
			m.Usage.ServiceTier, costUSD, boolToInt(c.Known),
		); err != nil {
			return fmt.Errorf("usage_events(%s, seq=%d) の保存に失敗: %w", s.SessionID, m.Seq, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("トランザクションのコミットに失敗: %w", err)
	}
	return nil
}

// --- 増分取り込み状態 ---

// NeedsIngest は ingest_state と突き合わせ、取り込みが必要かどうかを返す。
// 未登録、または mtime/size のいずれかが記録済みの値と異なれば true。
func (d *DB) NeedsIngest(source, path string, mtime time.Time, size int64) (bool, error) {
	var storedMtime string
	var storedSize int64
	err := d.db.QueryRow(
		`SELECT mtime, size FROM ingest_state WHERE source = ? AND path = ?`,
		source, path,
	).Scan(&storedMtime, &storedSize)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("ingest_state(%s, %s) の取得に失敗: %w", source, path, err)
	}
	if storedSize != size || storedMtime != toUTCString(mtime) {
		return true, nil
	}
	return false, nil
}

// MarkIngested は取り込み済みとして ingest_state に記録する（同じキーなら上書き）。
func (d *DB) MarkIngested(source, path string, mtime time.Time, size int64, contentHash string) error {
	now := time.Now().UTC().Format(timeLayout)
	_, err := d.db.Exec(`
		INSERT INTO ingest_state (source, path, mtime, size, content_hash, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, path) DO UPDATE SET
			mtime        = excluded.mtime,
			size         = excluded.size,
			content_hash = excluded.content_hash,
			ingested_at  = excluded.ingested_at
	`, source, path, toUTCString(mtime), size, contentHash, now)
	if err != nil {
		return fmt.Errorf("ingest_state(%s, %s) の保存に失敗: %w", source, path, err)
	}
	return nil
}

// LastIngestAt は最後に取り込みが行われた時刻を返す。未取り込みならゼロ値。doctor が使う。
func (d *DB) LastIngestAt() (time.Time, error) {
	var s sql.NullString
	if err := d.db.QueryRow(`SELECT MAX(ingested_at) FROM ingest_state`).Scan(&s); err != nil {
		return time.Time{}, fmt.Errorf("最終取り込み時刻の取得に失敗: %w", err)
	}
	if !s.Valid || s.String == "" {
		return time.Time{}, nil
	}
	return parseUTCString(s.String)
}

// --- 評価キャッシュ ---

// SaveEval は判定結果を (session_id, prompt_version) 単位でキャッシュする。
// run にはその評価にかかった実コストと claude 実行の session_id を渡す（取得できない
// バックエンドではゼロ値でよい）。日報がどの経路で評価しても同じ評価コストを出せるよう、
// 評価結果と同じ行に残す。
func (d *DB) SaveEval(sessionID, judge, judgeModel, promptVersion, contentHash string, eval json.RawMessage, run EvalRun) error {
	now := time.Now().UTC().Format(timeLayout)
	_, err := d.db.Exec(`
		INSERT INTO session_evals (
			session_id, judge, judge_model, prompt_version, content_hash, eval_json, created_at,
			cost_usd, run_session_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, prompt_version) DO UPDATE SET
			judge          = excluded.judge,
			judge_model    = excluded.judge_model,
			content_hash   = excluded.content_hash,
			eval_json      = excluded.eval_json,
			created_at     = excluded.created_at,
			cost_usd       = excluded.cost_usd,
			run_session_id = excluded.run_session_id
	`, sessionID, judge, judgeModel, promptVersion, contentHash, string(eval), now, run.CostUSD, run.SessionID)
	if err != nil {
		return fmt.Errorf("session_evals(%s, %s) の保存に失敗: %w", sessionID, promptVersion, err)
	}
	return nil
}

// EvalFor はキャッシュ済みの評価を返す。content_hash が保存時と異なる場合は
// セッション内容が変わった（トランスクリプトが再取り込みされた等）とみなし、見つからない扱いにする。
func (d *DB) EvalFor(sessionID, promptVersion, contentHash string) (json.RawMessage, bool, error) {
	var storedHash, evalJSON string
	err := d.db.QueryRow(
		`SELECT content_hash, eval_json FROM session_evals WHERE session_id = ? AND prompt_version = ?`,
		sessionID, promptVersion,
	).Scan(&storedHash, &evalJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("session_evals(%s, %s) の取得に失敗: %w", sessionID, promptVersion, err)
	}
	if storedHash != contentHash {
		return nil, false, nil
	}
	return json.RawMessage(evalJSON), true, nil
}

// EvalRunTotals は指定セッションの評価にかかった実コストの合計と、その評価を行った
// claude 実行の session_id 一覧を返す。評価がまだ無いセッションは単に無視する。
//
// 日報の meta.judge_cost_usd / meta.judge_session_ids はこの値を使う。その場の実行で
// 評価した分だけを数えると、`insights judge` で先に評価を済ませてから日報を作る経路
// （`insights run` を含む）では常に 0 になり、評価コストの自己監視が働かなくなるため、
// 実行結果ではなく DB から引き直す。
// 返す session_id 一覧は、日報 JSON の差分が並び順で揺れないよう session_id 順に固定する。
func (d *DB) EvalRunTotals(sessionIDs []string, promptVersion string) (float64, []string, error) {
	if len(sessionIDs) == 0 {
		return 0, nil, nil
	}

	args := make([]any, 0, len(sessionIDs)+1)
	args = append(args, promptVersion)
	for _, id := range sessionIDs {
		args = append(args, id)
	}
	placeholders := strings.TrimPrefix(strings.Repeat(",?", len(sessionIDs)), ",")

	rows, err := d.db.Query(
		`SELECT cost_usd, run_session_id FROM session_evals WHERE prompt_version = ? AND session_id IN (`+placeholders+`) ORDER BY session_id`,
		args...)
	if err != nil {
		return 0, nil, fmt.Errorf("session_evals の評価コスト取得に失敗: %w", err)
	}
	defer rows.Close()

	var total float64
	var runIDs []string
	for rows.Next() {
		var cost float64
		var runID string
		if err := rows.Scan(&cost, &runID); err != nil {
			return 0, nil, fmt.Errorf("session_evals の評価コスト読み取りに失敗: %w", err)
		}
		total += cost
		if strings.TrimSpace(runID) != "" {
			runIDs = append(runIDs, runID)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("session_evals の走査に失敗: %w", err)
	}
	return total, runIDs, nil
}

// SaveEvalRun は評価 1 回分の実行記録を追記する。成功・失敗のどちらも残す。
// 記録に失敗しても評価そのものは成立しているため、呼び出し側はこのエラーで
// 評価を失敗扱いにしないこと（監視用の記録であって、評価結果の保存ではない）。
func (d *DB) SaveEvalRun(r EvalRunRecord) error {
	ok := 0
	if r.OK {
		ok = 1
	}
	_, err := d.db.Exec(`
		INSERT INTO eval_runs (
			session_id, prompt_version, judge, judge_model, ok,
			failure_kind, failure_reason, cost_usd, run_session_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.SessionID, r.PromptVersion, r.Judge, r.JudgeModel, ok,
		r.FailureKind, r.FailureReason, r.CostUSD, r.RunSessionID,
		time.Now().UTC().Format(timeLayout))
	if err != nil {
		return fmt.Errorf("eval_runs(%s) の保存に失敗: %w", r.SessionID, err)
	}
	return nil
}

// EvalRunStatsInRange は from〜to（created_at 基準）に行われた評価実行の集計を返す。
// 期間レポートに「評価自体が健全に回っているか」を出すために使う。
func (d *DB) EvalRunStatsInRange(from, to time.Time) (EvalRunStats, error) {
	stats := EvalRunStats{FailuresByKind: map[string]int{}}
	rows, err := d.db.Query(
		`SELECT ok, failure_kind, cost_usd FROM eval_runs WHERE created_at >= ? AND created_at < ?`,
		toUTCString(from), toUTCString(to))
	if err != nil {
		return stats, fmt.Errorf("eval_runs の取得に失敗: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ok int
		var kind string
		var cost float64
		if err := rows.Scan(&ok, &kind, &cost); err != nil {
			return stats, fmt.Errorf("eval_runs の読み取りに失敗: %w", err)
		}
		stats.Total++
		stats.CostUSD += cost
		if ok == 1 {
			stats.Succeeded++
			continue
		}
		stats.Failed++
		if kind == "" {
			kind = EvalFailureOther
		}
		stats.FailuresByKind[kind]++
	}
	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("eval_runs の走査に失敗: %w", err)
	}
	return stats, nil
}

// RecentEvalCostSamples は成功した評価の実コストを新しい順に最大 limit 件返す。
// コストを取得できないバックエンド（cost_usd = 0）は見積もりの材料にならないので除く。
//
// 新しい順に限るのは、モデルの単価改定やプロンプトの変更で実コストが変わるため。
// 古い実績まで平等に混ぜると、見積もりが現状から離れていく。
func (d *DB) RecentEvalCostSamples(judgeModel string, limit int) ([]EvalCostSample, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := d.db.Query(`
		SELECT r.cost_usd, COALESCE(s.message_count, 0)
		FROM eval_runs r
		LEFT JOIN sessions s ON s.session_id = r.session_id
		WHERE r.ok = 1 AND r.cost_usd > 0 AND r.judge_model = ?
		ORDER BY r.id DESC
		LIMIT ?`, judgeModel, limit)
	if err != nil {
		return nil, fmt.Errorf("評価コスト実績の取得に失敗: %w", err)
	}
	defer rows.Close()

	var out []EvalCostSample
	for rows.Next() {
		var sample EvalCostSample
		if err := rows.Scan(&sample.CostUSD, &sample.MessageCount); err != nil {
			return nil, fmt.Errorf("評価コスト実績の読み取りに失敗: %w", err)
		}
		out = append(out, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("評価コスト実績の走査に失敗: %w", err)
	}
	return out, nil
}

// UnevaluatedSessions は from〜to に開始した中で、指定した prompt_version の評価が
// まだ無い、または content_hash が変わっていて再評価が必要なセッション ID を返す。
func (d *DB) UnevaluatedSessions(from, to time.Time, promptVersion string) ([]string, error) {
	rows, err := d.db.Query(`
		SELECT s.session_id
		FROM sessions s
		LEFT JOIN session_evals e
			ON e.session_id = s.session_id AND e.prompt_version = ?
		WHERE s.started_at >= ? AND s.started_at <= ?
		  AND (e.session_id IS NULL OR e.content_hash != s.content_hash)
		ORDER BY s.started_at
	`, promptVersion, toUTCString(from), toUTCString(to))
	if err != nil {
		return nil, fmt.Errorf("未評価セッションの取得に失敗: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("session_id の読み取りに失敗: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("未評価セッションの走査に失敗: %w", err)
	}
	return ids, nil
}

// --- 参照 ---

// SessionsInRange は started_at が [from, to] に含まれるセッションを開始時刻順に返す。
func (d *DB) SessionsInRange(from, to time.Time) ([]SessionRow, error) {
	rows, err := d.db.Query(`
		SELECT session_id, project_label, project_path, started_at, ended_at, is_sidechain,
		       parent_session_id, entrypoint, first_prompt, title, message_count, tool_error_count, content_hash
		FROM sessions
		WHERE started_at >= ? AND started_at <= ?
		ORDER BY started_at
	`, toUTCString(from), toUTCString(to))
	if err != nil {
		return nil, fmt.Errorf("sessions の取得に失敗: %w", err)
	}
	defer rows.Close()

	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var startedAt, endedAt string
		var isSidechain int
		if err := rows.Scan(
			&r.SessionID, &r.ProjectLabel, &r.ProjectPath, &startedAt, &endedAt, &isSidechain,
			&r.ParentSessionID, &r.Entrypoint, &r.FirstPrompt, &r.Title, &r.MessageCount, &r.ToolErrorCount, &r.ContentHash,
		); err != nil {
			return nil, fmt.Errorf("sessions 行の読み取りに失敗: %w", err)
		}
		r.IsSidechain = isSidechain != 0
		if r.StartedAt, err = parseUTCString(startedAt); err != nil {
			return nil, err
		}
		if r.EndedAt, err = parseUTCString(endedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessions の走査に失敗: %w", err)
	}
	return out, nil
}

// SessionByID はセッション本体をメッセージ（使用量込み）まで復元する。
func (d *DB) SessionByID(id string) (*model.Session, error) {
	var s model.Session
	var startedAt, endedAt string
	var isSidechain int
	err := d.db.QueryRow(`
		SELECT session_id, source, project_path, project_label, git_branch, entrypoint,
		       is_sidechain, parent_session_id, started_at, ended_at, first_prompt, title,
		       transcript_path, content_hash
		FROM sessions WHERE session_id = ?
	`, id).Scan(
		&s.SessionID, &s.Source, &s.ProjectPath, &s.ProjectLabel, &s.GitBranch, &s.Entrypoint,
		&isSidechain, &s.ParentSessionID, &startedAt, &endedAt, &s.FirstPrompt, &s.Title,
		&s.TranscriptPath, &s.ContentHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("session %q は見つからない: %w", id, err)
	}
	if err != nil {
		return nil, fmt.Errorf("session %q の取得に失敗: %w", id, err)
	}
	s.IsSidechain = isSidechain != 0
	if s.StartedAt, err = parseUTCString(startedAt); err != nil {
		return nil, err
	}
	if s.EndedAt, err = parseUTCString(endedAt); err != nil {
		return nil, err
	}

	// usage_events を先に seq -> *model.Usage のマップへ読み込み、messages と結合する。
	usageBySeq := make(map[int]*model.Usage)
	urows, err := d.db.Query(`
		SELECT seq, input_tokens, output_tokens, thinking_tokens, cache_creation_5m,
		       cache_creation_1h, cache_read, service_tier
		FROM usage_events WHERE session_id = ?
	`, id)
	if err != nil {
		return nil, fmt.Errorf("usage_events(%s) の取得に失敗: %w", id, err)
	}
	for urows.Next() {
		var seq int
		var u model.Usage
		if err := urows.Scan(&seq, &u.InputTokens, &u.OutputTokens, &u.ThinkingTokens,
			&u.CacheCreation5m, &u.CacheCreation1h, &u.CacheRead, &u.ServiceTier); err != nil {
			urows.Close()
			return nil, fmt.Errorf("usage_events(%s) 行の読み取りに失敗: %w", id, err)
		}
		usageBySeq[seq] = &u
	}
	if err := urows.Err(); err != nil {
		urows.Close()
		return nil, fmt.Errorf("usage_events(%s) の走査に失敗: %w", id, err)
	}
	urows.Close()

	mrows, err := d.db.Query(`
		SELECT seq, ts, role, model, effort, text, truncated, tool_name, is_error, is_meta
		FROM messages WHERE session_id = ? ORDER BY seq
	`, id)
	if err != nil {
		return nil, fmt.Errorf("messages(%s) の取得に失敗: %w", id, err)
	}
	defer mrows.Close()

	for mrows.Next() {
		var m model.Message
		var ts, role string
		var truncated, isError, isMeta int
		if err := mrows.Scan(&m.Seq, &ts, &role, &m.Model, &m.Effort, &m.Text, &truncated,
			&m.ToolName, &isError, &isMeta); err != nil {
			return nil, fmt.Errorf("messages(%s) 行の読み取りに失敗: %w", id, err)
		}
		m.Role = model.Role(role)
		m.Truncated = truncated != 0
		m.IsError = isError != 0
		m.IsMeta = isMeta != 0
		if m.Timestamp, err = parseUTCString(ts); err != nil {
			return nil, err
		}
		if u, ok := usageBySeq[m.Seq]; ok {
			m.Usage = u
		}
		s.Messages = append(s.Messages, m)
	}
	if err := mrows.Err(); err != nil {
		return nil, fmt.Errorf("messages(%s) の走査に失敗: %w", id, err)
	}

	return &s, nil
}

// UsageInRange は ts が [from, to] に含まれる使用量イベントを時刻順に返す。
func (d *DB) UsageInRange(from, to time.Time) ([]UsageRow, error) {
	rows, err := d.db.Query(`
		SELECT session_id, ts, model, input_tokens, output_tokens, thinking_tokens,
		       cache_creation_5m, cache_creation_1h, cache_read, cost_usd, cost_known
		FROM usage_events
		WHERE ts >= ? AND ts <= ?
		ORDER BY ts
	`, toUTCString(from), toUTCString(to))
	if err != nil {
		return nil, fmt.Errorf("usage_events の取得に失敗: %w", err)
	}
	defer rows.Close()

	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		var ts string
		var costKnown int
		if err := rows.Scan(&r.SessionID, &ts, &r.Model, &r.InputTokens, &r.OutputTokens,
			&r.ThinkingTokens, &r.CacheCreation5m, &r.CacheCreation1h, &r.CacheRead,
			&r.CostUSD, &costKnown); err != nil {
			return nil, fmt.Errorf("usage_events 行の読み取りに失敗: %w", err)
		}
		r.CostKnown = costKnown != 0
		if r.Timestamp, err = parseUTCString(ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage_events の走査に失敗: %w", err)
	}
	return out, nil
}

// SaveEvidence は (session_id, kind, ref) をキーに UPSERT する。
func (d *DB) SaveEvidence(items []model.Evidence) error {
	if len(items) == 0 {
		return nil
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("トランザクション開始に失敗: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO evidence (session_id, kind, ref, ts, title, body, insertions, deletions, files)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, kind, ref) DO UPDATE SET
			ts         = excluded.ts,
			title      = excluded.title,
			body       = excluded.body,
			insertions = excluded.insertions,
			deletions  = excluded.deletions,
			files      = excluded.files
	`)
	if err != nil {
		return fmt.Errorf("evidence の準備に失敗: %w", err)
	}
	defer stmt.Close()

	for _, e := range items {
		if _, err := stmt.Exec(e.SessionID, e.Kind, e.Ref, toUTCString(e.Timestamp), e.Title, e.Body,
			e.Insertions, e.Deletions, e.Files); err != nil {
			return fmt.Errorf("evidence(%s, %s, %s) の保存に失敗: %w", e.SessionID, e.Kind, e.Ref, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("トランザクションのコミットに失敗: %w", err)
	}
	return nil
}

// EvidenceFor は 1 セッションに紐づく Evidence を時刻順に返す。
func (d *DB) EvidenceFor(sessionID string) ([]model.Evidence, error) {
	rows, err := d.db.Query(`
		SELECT session_id, kind, ref, ts, title, body, insertions, deletions, files
		FROM evidence WHERE session_id = ? ORDER BY ts
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("evidence(%s) の取得に失敗: %w", sessionID, err)
	}
	defer rows.Close()

	var out []model.Evidence
	for rows.Next() {
		var e model.Evidence
		var ts string
		if err := rows.Scan(&e.SessionID, &e.Kind, &e.Ref, &ts, &e.Title, &e.Body,
			&e.Insertions, &e.Deletions, &e.Files); err != nil {
			return nil, fmt.Errorf("evidence(%s) 行の読み取りに失敗: %w", sessionID, err)
		}
		if e.Timestamp, err = parseUTCString(ts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evidence(%s) の走査に失敗: %w", sessionID, err)
	}
	return out, nil
}

// --- 日次ロールアップ ---

// SaveRollup は日次ロールアップ（JSON）を date をキーに UPSERT する。
func (d *DB) SaveRollup(date string, rollup json.RawMessage, dailyPath, retroPath string) error {
	now := time.Now().UTC().Format(timeLayout)
	_, err := d.db.Exec(`
		INSERT INTO daily_rollups (date, rollup_json, daily_path, retro_path, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET
			rollup_json = excluded.rollup_json,
			daily_path  = excluded.daily_path,
			retro_path  = excluded.retro_path,
			created_at  = excluded.created_at
	`, date, string(rollup), dailyPath, retroPath, now)
	if err != nil {
		return fmt.Errorf("daily_rollups(%s) の保存に失敗: %w", date, err)
	}
	return nil
}

// Rollup は date のロールアップ JSON を返す。無ければ ok=false。
func (d *DB) Rollup(date string) (json.RawMessage, bool, error) {
	var raw string
	err := d.db.QueryRow(`SELECT rollup_json FROM daily_rollups WHERE date = ?`, date).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("daily_rollups(%s) の取得に失敗: %w", date, err)
	}
	return json.RawMessage(raw), true, nil
}

// RollupsInRange は date が [from, to]（YYYY-MM-DD の文字列比較）に含まれるロールアップを日付順に返す。
func (d *DB) RollupsInRange(from, to string) ([]RollupRow, error) {
	rows, err := d.db.Query(`
		SELECT date, rollup_json, daily_path, retro_path, created_at
		FROM daily_rollups WHERE date >= ? AND date <= ? ORDER BY date
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("daily_rollups の取得に失敗: %w", err)
	}
	defer rows.Close()

	var out []RollupRow
	for rows.Next() {
		var r RollupRow
		var raw, createdAt string
		if err := rows.Scan(&r.Date, &raw, &r.DailyPath, &r.RetroPath, &createdAt); err != nil {
			return nil, fmt.Errorf("daily_rollups 行の読み取りに失敗: %w", err)
		}
		r.RollupJSON = json.RawMessage(raw)
		if r.CreatedAt, err = parseUTCString(createdAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("daily_rollups の走査に失敗: %w", err)
	}
	return out, nil
}

// --- 改善アクション ---

// CreateAction は改善提案を 1 件追加し、採番された ID を返す。Status が空なら ActionOpen とする。
func (d *DB) CreateAction(a *model.Action) (int64, error) {
	if a == nil {
		return 0, fmt.Errorf("CreateAction: action が nil")
	}
	status := a.Status
	if status == "" {
		status = model.ActionOpen
	}
	res, err := d.db.Exec(`
		INSERT INTO actions (created_on, title, detail, category, status, verdict, verified_on)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, a.CreatedOn, a.Title, a.Detail, a.Category, string(status), a.Verdict, a.VerifiedOn)
	if err != nil {
		return 0, fmt.Errorf("actions の保存に失敗: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("actions の ID 取得に失敗: %w", err)
	}
	return id, nil
}

// UpdateActionStatus は 1 件の改善アクションの状態・検証結果を更新する。
func (d *DB) UpdateActionStatus(id int64, status model.ActionStatus, verdict, verifiedOn string) error {
	if _, err := d.db.Exec(
		`UPDATE actions SET status = ?, verdict = ?, verified_on = ? WHERE id = ?`,
		string(status), verdict, verifiedOn, id,
	); err != nil {
		return fmt.Errorf("action(id=%d) の状態更新に失敗: %w", id, err)
	}
	return nil
}

// ActionsByStatus は指定した状態のいずれかに一致する改善アクションを返す。
func (d *DB) ActionsByStatus(statuses ...model.ActionStatus) ([]model.Action, error) {
	if len(statuses) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, st := range statuses {
		placeholders[i] = "?"
		args[i] = string(st)
	}
	// IN 句のプレースホルダ個数は statuses の長さに応じて動的に組み立てるが、
	// 値自体は必ずプレースホルダ経由で渡す（文字列連結しない）。
	query := `
		SELECT id, created_on, title, detail, category, status, verdict, verified_on
		FROM actions WHERE status IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY created_on, id
	`

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("actions の取得に失敗: %w", err)
	}
	defer rows.Close()
	return scanActions(rows)
}

// AllActions は全ての改善アクションを返す。
func (d *DB) AllActions() ([]model.Action, error) {
	rows, err := d.db.Query(`
		SELECT id, created_on, title, detail, category, status, verdict, verified_on
		FROM actions ORDER BY created_on, id
	`)
	if err != nil {
		return nil, fmt.Errorf("actions の取得に失敗: %w", err)
	}
	defer rows.Close()
	return scanActions(rows)
}

// scanActions は actions テーブルの SELECT 結果を []model.Action に変換する共通処理。
func scanActions(rows *sql.Rows) ([]model.Action, error) {
	var out []model.Action
	for rows.Next() {
		var a model.Action
		var status string
		if err := rows.Scan(&a.ID, &a.CreatedOn, &a.Title, &a.Detail, &a.Category, &status,
			&a.Verdict, &a.VerifiedOn); err != nil {
			return nil, fmt.Errorf("actions 行の読み取りに失敗: %w", err)
		}
		a.Status = model.ActionStatus(status)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("actions の走査に失敗: %w", err)
	}
	return out, nil
}
