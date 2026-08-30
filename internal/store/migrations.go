package store

// migration はスキーマに対する 1 回限りの変更。version は 1 始まりの連番で、
// 適用済みかどうかは schema_migrations テーブルで管理する。
// 将来スキーマを変える際は、既存の要素を書き換えず migrations の末尾に追記すること。
type migration struct {
	version int
	sql     string
}

// migrations は適用順のスキーマ変更一覧。
var migrations = []migration{
	{version: 1, sql: schemaV1},
	{version: 2, sql: schemaV2},
	{version: 3, sql: schemaV3},
}

// schemaV1 は初期スキーマ。insights が正規化データを保存する全テーブルをここで作る。
const schemaV1 = `
CREATE TABLE IF NOT EXISTS sessions (
	session_id               TEXT PRIMARY KEY,
	source                   TEXT NOT NULL,
	project_path             TEXT NOT NULL DEFAULT '',
	project_label            TEXT NOT NULL DEFAULT '',
	git_branch               TEXT NOT NULL DEFAULT '',
	entrypoint               TEXT NOT NULL DEFAULT '',
	is_sidechain             INTEGER NOT NULL DEFAULT 0,
	parent_session_id        TEXT NOT NULL DEFAULT '',
	started_at               TEXT NOT NULL DEFAULT '',
	ended_at                 TEXT NOT NULL DEFAULT '',
	first_prompt             TEXT NOT NULL DEFAULT '',
	title                    TEXT NOT NULL DEFAULT '',
	transcript_path          TEXT NOT NULL DEFAULT '',
	content_hash             TEXT NOT NULL DEFAULT '',
	message_count            INTEGER NOT NULL DEFAULT 0,
	user_message_count       INTEGER NOT NULL DEFAULT 0,
	assistant_message_count  INTEGER NOT NULL DEFAULT 0,
	tool_error_count         INTEGER NOT NULL DEFAULT 0,
	ingested_at              TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS messages (
	session_id  TEXT NOT NULL,
	seq         INTEGER NOT NULL,
	ts          TEXT NOT NULL DEFAULT '',
	role        TEXT NOT NULL DEFAULT '',
	model       TEXT NOT NULL DEFAULT '',
	effort      TEXT NOT NULL DEFAULT '',
	text        TEXT NOT NULL DEFAULT '',
	truncated   INTEGER NOT NULL DEFAULT 0,
	tool_name   TEXT NOT NULL DEFAULT '',
	is_error    INTEGER NOT NULL DEFAULT 0,
	is_meta     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS usage_events (
	session_id         TEXT NOT NULL,
	seq                INTEGER NOT NULL,
	ts                 TEXT NOT NULL DEFAULT '',
	model              TEXT NOT NULL DEFAULT '',
	input_tokens       INTEGER NOT NULL DEFAULT 0,
	output_tokens      INTEGER NOT NULL DEFAULT 0,
	thinking_tokens    INTEGER NOT NULL DEFAULT 0,
	cache_creation_5m  INTEGER NOT NULL DEFAULT 0,
	cache_creation_1h  INTEGER NOT NULL DEFAULT 0,
	cache_read         INTEGER NOT NULL DEFAULT 0,
	service_tier       TEXT NOT NULL DEFAULT '',
	cost_usd           REAL NOT NULL DEFAULT 0,
	cost_known         INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_id, seq)
);

CREATE TABLE IF NOT EXISTS evidence (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id  TEXT NOT NULL,
	kind        TEXT NOT NULL,
	ref         TEXT NOT NULL,
	ts          TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	body        TEXT NOT NULL DEFAULT '',
	insertions  INTEGER NOT NULL DEFAULT 0,
	deletions   INTEGER NOT NULL DEFAULT 0,
	files       INTEGER NOT NULL DEFAULT 0,
	UNIQUE (session_id, kind, ref)
);

CREATE TABLE IF NOT EXISTS session_evals (
	session_id      TEXT NOT NULL,
	judge           TEXT NOT NULL DEFAULT '',
	judge_model     TEXT NOT NULL DEFAULT '',
	prompt_version  TEXT NOT NULL,
	content_hash    TEXT NOT NULL DEFAULT '',
	eval_json       TEXT NOT NULL DEFAULT '',
	created_at      TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, prompt_version)
);

CREATE TABLE IF NOT EXISTS daily_rollups (
	date         TEXT PRIMARY KEY,
	rollup_json  TEXT NOT NULL DEFAULT '',
	daily_path   TEXT NOT NULL DEFAULT '',
	retro_path   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS actions (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	created_on   TEXT NOT NULL,
	title        TEXT NOT NULL,
	detail       TEXT NOT NULL DEFAULT '',
	category     TEXT NOT NULL DEFAULT '',
	status       TEXT NOT NULL,
	verdict      TEXT NOT NULL DEFAULT '',
	verified_on  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS ingest_state (
	source        TEXT NOT NULL,
	path          TEXT NOT NULL,
	mtime         TEXT NOT NULL DEFAULT '',
	size          INTEGER NOT NULL DEFAULT 0,
	content_hash  TEXT NOT NULL DEFAULT '',
	ingested_at   TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (source, path)
);

CREATE INDEX IF NOT EXISTS idx_sessions_started_at   ON sessions(started_at);
CREATE INDEX IF NOT EXISTS idx_sessions_project_path ON sessions(project_path);
CREATE INDEX IF NOT EXISTS idx_usage_events_ts        ON usage_events(ts);
CREATE INDEX IF NOT EXISTS idx_messages_session_id    ON messages(session_id);
`

// schemaV2 は評価 1 件ごとの実コストと、その評価を行った claude 実行の session_id を
// session_evals に持たせる。
//
// これが無いと「評価にいくら使ったか」はその場の実行結果にしか残らず、評価を
// `insights judge` で先に済ませてから日報を作る経路（`insights run` を含む）では
// 日報の meta.judge_cost_usd が常に 0 になる。振り返りのコストを自己監視するための
// 数字なので、評価結果と同じ行に永続化して、どの経路で評価しても同じ値が出るようにする。
const schemaV2 = `
ALTER TABLE session_evals ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0;
ALTER TABLE session_evals ADD COLUMN run_session_id TEXT NOT NULL DEFAULT '';
`

// schemaV3 は評価の実行記録を残すテーブル。session_evals が「最新の評価結果」を
// (session_id, prompt_version) で 1 行に上書きするのに対し、こちらは試行を追記していく。
//
// 失敗した評価は session_evals に何も残らないため、成功した結果だけを見ていても
// 「特定の形のセッションで失敗し続けている」ことに気づけない。評価そのものが
// 本末転倒になっていないかを自己監視するのがこのツールの前提なので、評価自体の
// 健全性を後から見られるようにする。
const schemaV3 = `
CREATE TABLE IF NOT EXISTS eval_runs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id      TEXT NOT NULL,
	prompt_version  TEXT NOT NULL DEFAULT '',
	judge           TEXT NOT NULL DEFAULT '',
	judge_model     TEXT NOT NULL DEFAULT '',
	ok              INTEGER NOT NULL DEFAULT 0,
	failure_kind    TEXT NOT NULL DEFAULT '',
	failure_reason  TEXT NOT NULL DEFAULT '',
	cost_usd        REAL NOT NULL DEFAULT 0,
	run_session_id  TEXT NOT NULL DEFAULT '',
	created_at      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_eval_runs_created_at ON eval_runs(created_at);
`
