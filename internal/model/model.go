// Package model はログソースやAIバックエンドに依存しない正規化データモデルを定義する。
// Claude Code / Codex など由来の異なるセッションを同じ形に落として扱うための共通語彙。
package model

import (
	"strings"
	"time"
)

// Role はメッセージの発話者種別。
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool_result"
	RoleSystem    Role = "system"
)

// Session は 1 セッション分の正規化済みトランスクリプト。
type Session struct {
	Source       string // "claude-code" | "codex"
	SessionID    string // ソース内で一意
	ProjectPath  string // 作業ディレクトリの絶対パス
	ProjectLabel string // 表示用の短縮名（末尾ディレクトリ名など）
	GitBranch    string // 不明なら空
	// Worktree はワークツリー配下のセッションのときだけ入るワークツリー名。
	// ProjectPath は元のプロジェクト（リポジトリ）に寄せてあるため、どの
	// ワークツリーでの作業だったかはこちらに残す。
	Worktree        string
	Entrypoint      string    // "cli" | "sdk-cli" など。自動実行の判別に使う
	IsSidechain     bool      // サブエージェント実行
	ParentSessionID string    // sidechain の親。不明なら空
	StartedAt       time.Time // 最初のレコードの時刻
	EndedAt         time.Time // 最後のレコードの時刻
	FirstPrompt     string    // 最初のユーザー発話（切り詰め済み）
	Title           string    // ソースが持っていれば。無ければ空
	TranscriptPath  string    // 取り込み元ファイル（消える前提の参考値）
	ContentHash     string    // 本文由来のハッシュ。評価キャッシュの冪等キー
	Messages        []Message
}

// Duration はセッションの実時間。
func (s *Session) Duration() time.Duration { return s.EndedAt.Sub(s.StartedAt) }

// Message は 1 発話。ツール呼び出し・ツール結果も 1 行として持つ。
type Message struct {
	Seq       int
	Timestamp time.Time
	Role      Role
	Model     string // assistant のみ。例 "claude-sonnet-5"
	Effort    string // assistant のみ。例 "high"
	Text      string // 本文。ツール結果は取り込み時に切り詰める
	Truncated bool   // Text が切り詰められたか
	ToolName  string // ツール呼び出し／結果のときのみ
	IsError   bool   // ツールエラー
	IsMeta    bool   // system-reminder やコマンド出力など、人間の発話でないもの
	Usage     *Usage // assistant のみ
}

// Usage は 1 アシスタント応答のトークン使用量。
type Usage struct {
	InputTokens     int
	OutputTokens    int
	ThinkingTokens  int
	CacheCreation5m int
	CacheCreation1h int
	CacheRead       int
	ServiceTier     string
}

// Evidence はセッションの外側にある成果物の痕跡（コミット・PR など）。
type Evidence struct {
	SessionID  string
	Kind       string // "commit" | "pr" | "issue" | "mr"
	Ref        string // SHA / URL / 番号
	Timestamp  time.Time
	Title      string
	Body       string
	Insertions int
	Deletions  int
	Files      int
}

// Eval は AI によるセッション単位の定性評価。judge のプロンプトが返す JSON と 1:1 で対応する。
type Eval struct {
	UnderlyingGoal   string        `json:"underlying_goal"`
	GoalCategory     string        `json:"goal_category"`  // feature|bugfix|research|automation|writing|ops|learning|other
	Outcome          string        `json:"outcome"`        // achieved|partial|abandoned|exploratory
	ArtifactValue    string        `json:"artifact_value"` // durable|transient|none
	InterventionCost Assessment    `json:"intervention_cost"`
	Rework           Rework        `json:"rework"`
	ModelFit         VerdictReason `json:"model_fit"`      // verdict: over|appropriate|under
	Ownership        LevelReason   `json:"ownership"`      // level: understood|partial|black_box
	LearningValue    string        `json:"learning_value"` // none|some|high
	Friction         []string      `json:"friction"`
	OutcomeSummary   string        `json:"outcome_summary"`
	Confidence       string        `json:"confidence"` // low|medium|high
}

type Assessment struct {
	Level    string `json:"level"` // low|medium|high
	Evidence string `json:"evidence"`
}

type Rework struct {
	Occurred bool   `json:"occurred"`
	Cause    string `json:"cause"`
}

type VerdictReason struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

type LevelReason struct {
	Level  string `json:"level"`
	Reason string `json:"reason"`
}

// ActionStatus は改善提案の状態。
type ActionStatus string

const (
	ActionOpen    ActionStatus = "open"
	ActionDone    ActionStatus = "done"
	ActionDropped ActionStatus = "dropped"
	ActionExpired ActionStatus = "expired"
)

// Action は振り返りが生んだ改善提案と、その後の検証結果。
type Action struct {
	ID         int64
	CreatedOn  string // YYYY-MM-DD。提案された日
	Title      string
	Detail     string
	Category   string
	Status     ActionStatus
	Verdict    string // AI による検証所見
	VerifiedOn string // YYYY-MM-DD。最後に検証した日
}

// worktreeMarkers は Claude Code がワークツリーを切る場所。ワークツリーは
// <project>/.claude/worktree/<name> のようにプロジェクト配下に作られるため、
// cwd をそのままプロジェクトパスにすると 1 つのリポジトリの作業が
// ワークツリーの数だけ別プロジェクトに散ってしまう。
var worktreeMarkers = []string{"/.claude/worktree/", "/.claude/worktrees/"}

// SplitWorktreePath は cwd がワークツリー配下なら、元のプロジェクトのパスと
// ワークツリー名に分ける。ワークツリーでなければ (path, "") を返す。
//
// ワークツリーは「元のプロジェクトでの作業」として扱いたい（帰属先はリポジトリで
// あって作業用ディレクトリではない）。一方で、どのワークツリーでの作業だったかは
// 評価の文脈として残す価値があるので捨てずに返す。
func SplitWorktreePath(path string) (base, worktree string) {
	if path == "" {
		return "", ""
	}
	// cwd は記録した側のマシンの区切り文字で入っている。/ に寄せて探す
	// （置換は 1 バイト対 1 バイトなので、見つけた位置は元の path でもそのまま使える）。
	slashed := strings.ReplaceAll(path, `\`, "/")

	for _, marker := range worktreeMarkers {
		i := indexFold(slashed, marker)
		if i <= 0 {
			// 見つからない、または先頭一致（元のプロジェクトが空になる）は対象外。
			continue
		}
		rest := strings.Trim(slashed[i+len(marker):], "/")
		if rest == "" {
			continue
		}
		// ワークツリーのさらに下の階層が cwd のこともある。名前は先頭要素だけ。
		name := rest
		if j := strings.Index(name, "/"); j >= 0 {
			name = name[:j]
		}
		return strings.TrimRight(path[:i], `/\`), name
	}
	return path, ""
}

// indexFold は大文字小文字を無視して sub の位置を返す。見つからなければ -1。
// strings.ToLower で畳んでから探すと、ケース変換で長さが変わる文字（İ など）が
// パスに混ざったときに位置がずれるため、元の文字列上で走査する。
func indexFold(s, sub string) int {
	if sub == "" {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// nonInteractiveEntrypoints は「対話セッションではない」とみなす entrypoint の集合。
// 非対話とみなす entrypoint が増えたら、判定ロジックを触らずここに追記すればよい。
//
//   - sdk-cli: `claude -p`（Claude Agent SDK 経由の自動実行）が実データで名乗る値
//   - exec:    `codex exec`。人が同席しない一括実行のための入口
//   - mcp:     Codex を MCP サーバ／app-server として動かした場合。呼び出し元は
//     別のプログラムであり、実行中に人が軌道修正することはない
//
// Codex の内部スレッド（internal_*）とサブエージェント（subagent_*）もここに
// 該当するが、そちらは IsSidechain として親に畳み込まれるので入れていない。
var nonInteractiveEntrypoints = map[string]struct{}{
	"sdk-cli": {},
	"exec":    {},
	"mcp":     {},
}

// IsInteractiveEntrypoint は entrypoint が対話セッション（ユーザーが同席していて、
// 実行中に軌道修正も検収もできる状態）かどうかを判定する。entrypoint が空（不明）の
// ときは対話として扱う。
//
// 集計（対話/自動の内訳）と評価（実行形態による評価軸の読み替え）の両方が同じ境界を
// 使う必要があるため、どちらからも参照できる model に置いている。
func IsInteractiveEntrypoint(entrypoint string) bool {
	_, nonInteractive := nonInteractiveEntrypoints[strings.TrimSpace(entrypoint)]
	return !nonInteractive
}
