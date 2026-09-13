// Package check は個々の検査（doc-sync, unwanted-files など）が共通で使う型を定義する。
package check

// Granularity は範囲モードでの起動粒度。docs/hooks-extraction.md の「CI 側の範囲算出」節にある
// 「検査ごとに違う範囲の意味論」を表す。
type Granularity string

const (
	// GranularitySquashed は範囲全体を 1 回の比較としてまとめて見る。
	// 後からドキュメントを直すコミットを足せば通る（doc-sync 向け）。
	GranularitySquashed Granularity = "squashed"
	// GranularityPerCommit は範囲内のコミットごとに 1 回ずつ見る。
	// 後から消しても履歴に残るため直らない（unwanted-files, commit-subject 向け）。
	GranularityPerCommit Granularity = "per-commit"
	// GranularityWorktree は staged/range を問わず、現在の作業ツリーを 1 回だけ見る
	// （doc-paths 向け）。コミットメッセージにも依存しないため、免除トレーラも存在しない。
	GranularityWorktree Granularity = "worktree"
)

// Source は 1 回の比較（ステージ済み、またはある範囲）で検査が参照できるものを抽象化する。
// 検査本体は staged/range の違いを知る必要がない。GranularityWorktree の検査では使われない。
type Source interface {
	// ChangedFiles は追加・変更・コピー・改名されたファイルの一覧を返す（削除は含まない）。
	ChangedFiles() ([]string, error)
	// DiffLines はそのファイルの差分行（コンテキスト無し）を返す。
	DiffLines(path string) (string, error)
	// BlobSize はそのファイルの中身のバイト数を返す。
	BlobSize(path string) (int64, error)
}

// Context は 1 回の検査起動で検査本体に渡す入力をまとめる。
type Context struct {
	// Root はリポジトリのルート（カレントディレクトリからの相対、または絶対パス）。
	// GranularityWorktree の検査（doc-paths など）が現在のツリーを直接読むために使う。
	Root string
	// Source は staged/range の差分。GranularityWorktree の検査では nil。
	Source Source
	// Message はそのコミット（またはこれからコミットされる内容）のメッセージ本文。
	// 免除トレーラの判定はホスト（cli パッケージ）が済ませてから Run を呼ぶが、
	// commit-subject のようにメッセージの中身自体を検証したい検査はここから参照する。
	Message string
	// Range は staged/range どちらのモードかと、range モードでの生の比較両端。
	// command 型検査（外部プロセスに --from/--to を渡す必要がある）向け。
	// staged モードでは nil。
	Range *RangeRef
}

// RangeRef は range モードでの比較両端の生の git 参照。
type RangeRef struct {
	From string
	To   string
}

// Violation は 1 件の検査結果。
type Violation struct {
	// Summary は違反の見出し（例: "internal/cli/*.go を変更していますが、README.md が
	// 一緒に入っていません:"）。
	Summary string
	// Files はその違反に関係するファイル一覧。
	Files []string
}

// Runner は 1 つの検査インスタンスを表す。
type Runner interface {
	// Granularity はこの検査を範囲モードでどう起動するか。
	Granularity() Granularity
	// Run は 1 回の比較を検査し、違反があれば返す（無ければ空スライス）。
	Run(ctx Context) ([]Violation, error)
}
