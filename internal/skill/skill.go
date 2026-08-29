// Package skill は本ツールを任意のコーディングエージェントから使えるようにする
// スキルの配布を担う。エージェントごとの配置規約の違いを Installer で吸収する。
package skill

// Scope はスキルの導入範囲。
type Scope string

const (
	ScopeUser    Scope = "user"    // ユーザー全体（例 ~/.claude/skills）
	ScopeProject Scope = "project" // カレントプロジェクト（例 ./.claude/skills）
)

// State は導入済みスキルの状態。
type State string

const (
	StateAbsent   State = "absent"   // 未導入
	StateCurrent  State = "current"  // 同梱バージョンと一致
	StateOutdated State = "outdated" // 古いバージョンが入っている
	StateModified State = "modified" // 手で書き換えられている（--force が必要）
)

// Status は status サブコマンドの結果。
type Status struct {
	Agent            string
	Scope            Scope
	Path             string
	State            State
	InstalledVersion string
	BundledVersion   string
}

// Result は install の結果。
type Result struct {
	Path    string
	Written []string
	From    State
}

// Installer は 1 つのコーディングエージェント向けのスキル配置規約を表す。
type Installer interface {
	// Agent は "claude-code" のようなエージェント識別子。
	Agent() string

	// Detect はそのエージェントがこの環境に存在するかを返す。
	Detect() bool

	// Target は scope に対応する配置先ディレクトリを返す。
	Target(scope Scope) (string, error)

	// Install はスキルを配置する。State が Modified のとき force でなければエラー。
	Install(scope Scope, force bool) (Result, error)

	// Status は現在の導入状態を返す。
	Status(scope Scope) (Status, error)

	// Uninstall は配置済みスキルを削除する。未導入なら何もしない。
	Uninstall(scope Scope) error
}
