// Package source はログソースの抽象を定義する。
// Claude Code が唯一の実装だが、Codex など他のコーディングエージェントを
// 追加できるよう、発見とパースだけをインターフェースに切り出している。
package source

import (
	"time"

	"github.com/fuchigta/insights/internal/model"
)

// Ref は取り込み候補 1 件を指す。Discover が返し、Parse が受け取る。
type Ref struct {
	Source    string    // "claude-code"
	SessionID string    // ファイル名などから決まる一意 ID
	Path      string    // 取り込み元ファイルの絶対パス
	ModTime   time.Time // 増分取り込みの判定に使う
	Size      int64     // 同上
}

// Source は 1 つのコーディングエージェントのログ置き場を表す。
type Source interface {
	// Name は "claude-code" のようなソース識別子。
	Name() string

	// Available はログ置き場が存在し読める状態かを返す。doctor が使う。
	Available() error

	// Discover は since 以降に更新されたセッションを列挙する。
	// since がゼロ値なら全件。
	Discover(since time.Time) ([]Ref, error)

	// Parse は 1 セッションを正規化モデルへ変換する。
	// 壊れた行は握り潰さずスキップし、可能な限り読み進める。
	Parse(ref Ref) (*model.Session, error)
}
