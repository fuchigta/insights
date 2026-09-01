// Package codex は Codex CLI のロールアウトファイル
// ($CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl) を正規化データモデルへ変換する
// source.Source 実装。
//
// 【ログ置き場について】
// Codex は 1 スレッド（＝1 セッション）につき 1 本の JSONL を日付ごとのディレクトリに
// 書く。ファイル名は rollout-<YYYY-MM-DDThh-mm-ss>-<thread-id>.jsonl で、履歴を巻き戻した
// スレッドだけ <thread-id>_<rollout-id> と 2 つの ID が並ぶ。書き終えて 7 日以上経った
// ファイルは Codex 自身のバックグラウンド処理で .jsonl.zst（zstd）に圧縮されるため、
// 本実装は素の JSONL と圧縮版の両方を読む。
//
// アーカイブしたスレッドは archived_sessions/ に同じ構造で移るので、そちらも走査する
// （利用者がアーカイブしただけで、実際に行われた作業であることは変わらないため）。
package codex

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuchigta/insights/internal/source"
)

// sourceName は source.Source.Name() と source.Ref.Source に使う識別子。
const sourceName = "codex"

// DefaultMaxTextLen はツール呼び出し・ツール結果の本文を切り詰める既定の文字数。
// Source.MaxTextLen で上書きできる。claudecode と同じ値にしてあるのは、評価に渡る
// 情報量をログソースによって変えないため。
const DefaultMaxTextLen = 2000

// sessionsSubdir / archivedSessionsSubdir は Codex がロールアウトを置くサブディレクトリ。
// codex-rs/rollout の SESSIONS_SUBDIR / ARCHIVED_SESSIONS_SUBDIR に対応する。
const (
	sessionsSubdir         = "sessions"
	archivedSessionsSubdir = "archived_sessions"
)

// rolloutPrefix / plainSuffix / compressedSuffix はロールアウトのファイル名規約。
// codex-rs/rollout の parse_rollout_file_name と同じ判定をする。
const (
	rolloutPrefix    = "rollout-"
	plainSuffix      = ".jsonl"
	compressedSuffix = ".jsonl.zst"
)

// Source は Codex のログ置き場 1 つを表す source.Source 実装。
type Source struct {
	// Root は $CODEX_HOME 相当のディレクトリ。配下の sessions/ と
	// archived_sessions/ を見る。
	Root string
	// MaxTextLen はツール呼び出し・ツール結果本文の切り詰め文字数。
	// 0 以下なら DefaultMaxTextLen を使う。
	MaxTextLen int
}

// New は root を Codex のログ置き場として Source を作る。
// root が空文字なら $CODEX_HOME、それも無ければ ~/.codex を既定値として推定する。
func New(root string) *Source {
	if strings.TrimSpace(root) == "" {
		root = defaultRoot()
	}
	return &Source{
		Root:       root,
		MaxTextLen: DefaultMaxTextLen,
	}
}

// defaultRoot は Codex のホームディレクトリを推定する。
// Codex 自身が $CODEX_HOME を優先し、無ければ ~/.codex を使うため、それに合わせる
// （環境変数で移している利用者のログを取りこぼさないため）。
// ホームディレクトリが取得できない場合は空文字を返す
// （呼び出し側の Available() がエラーとして検出する）。
func defaultRoot() string {
	if env := strings.TrimSpace(os.Getenv("CODEX_HOME")); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// Name は "codex" を返す。
func (s *Source) Name() string {
	return sourceName
}

// SessionsDir は <root>/sessions の絶対パスを返す。
// 診断（insights config doctor）が「どこを見ているか」を表示するために公開している。
func (s *Source) SessionsDir() string {
	return filepath.Join(s.Root, sessionsSubdir)
}

// Available は <root>/sessions が存在し読める状態かを確認する。
func (s *Source) Available() error {
	if strings.TrimSpace(s.Root) == "" {
		return fmt.Errorf("codex のログ置き場のパスが決定できません（ホームディレクトリの取得に失敗した可能性があります）")
	}

	dir := s.SessionsDir()
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("codex のログ置き場が見つかりません (%s): %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("codex のログ置き場がディレクトリではありません: %s", dir)
	}
	if _, err := os.ReadDir(dir); err != nil {
		return fmt.Errorf("codex のログ置き場を読み取れません (%s): %w", dir, err)
	}
	return nil
}

// Discover は <root>/sessions と <root>/archived_sessions 配下のロールアウトを列挙する。
//
//	<root>/sessions/YYYY/MM/DD/rollout-<ts>-<thread-id>.jsonl[.zst]
//
// 日付ディレクトリの構造には依存せず再帰的に走査する（Codex 側でこの階層が変わっても
// 取りこぼさないため）。since がゼロ値でなければ ModTime が since 以降のものだけ返す。
// ディレクトリ単位の読み取り失敗は警告ログを出して他を続行する。
func (s *Source) Discover(since time.Time) ([]source.Ref, error) {
	roots := []string{s.SessionsDir(), filepath.Join(s.Root, archivedSessionsSubdir)}

	// 同じ論理ファイルの素の .jsonl と .jsonl.zst が並存しうる（圧縮中・展開直後）。
	// Codex 自身は素のほうを優先するので、こちらも ID 単位で 1 つに寄せる。
	byID := map[string]source.Ref{}
	var order []string

	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			// sessions/ が無い＝まだ 1 度も使っていない。archived_sessions/ は
			// アーカイブしたことが無ければそもそも作られない。どちらも異常ではない。
			continue
		}

		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				slog.Warn("codex: ディレクトリの走査に失敗しました", "path", path, "error", err)
				return nil // 一部が読めなくても他を続ける
			}
			if d.IsDir() {
				return nil
			}

			id, compressed, ok := parseRolloutFileName(d.Name())
			if !ok {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				slog.Warn("codex: ファイル情報の取得に失敗しました", "path", path, "error", err)
				return nil
			}
			if !since.IsZero() && info.ModTime().Before(since) {
				return nil
			}

			ref := source.Ref{
				Source:    sourceName,
				SessionID: id,
				Path:      path,
				ModTime:   info.ModTime(),
				Size:      info.Size(),
			}
			prev, exists := byID[id]
			switch {
			case !exists:
				order = append(order, id)
			case compressed:
				// 既に見つけているほうを優先する（素の .jsonl が既にあるか、
				// 同じ圧縮ファイルを二重に見ている）。
				return nil
			case strings.HasSuffix(prev.Path, compressedSuffix):
				// 素の .jsonl を見つけたので、圧縮版から乗り換える。
			default:
				// 素の .jsonl が 2 つ（別ディレクトリに同じ ID）という異常。
				// 先に見つけたほうを採用し、気付けるよう警告だけ残す。
				slog.Warn("codex: 同じ ID のロールアウトが複数見つかりました", "id", id, "kept", prev.Path, "skipped", path)
				return nil
			}
			byID[id] = ref
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("ロールアウトの走査に失敗しました (%s): %w", root, err)
		}
	}

	refs := make([]source.Ref, 0, len(byID))
	for _, id := range order {
		refs = append(refs, byID[id])
	}
	return refs, nil
}

// parseRolloutFileName はロールアウトのファイル名から ID 部分を取り出す。
// ロールアウトでなければ ok=false を返す。
//
// ID 部分は通常スレッド ID そのものだが、履歴を巻き戻したスレッドでは
// "<thread-id>_<rollout-id>" になる。ここでは分解せずそのまま使う。分解して
// スレッド ID だけを採ると、1 つのスレッドから生まれた複数のロールアウトが
// 同じセッション ID になり、DB（session_id が主キー）で上書きし合ってしまう。
func parseRolloutFileName(name string) (id string, compressed bool, ok bool) {
	rest, found := strings.CutPrefix(name, rolloutPrefix)
	if !found {
		return "", false, false
	}

	switch {
	case strings.HasSuffix(rest, compressedSuffix):
		rest, compressed = strings.TrimSuffix(rest, compressedSuffix), true
	case strings.HasSuffix(rest, plainSuffix):
		rest = strings.TrimSuffix(rest, plainSuffix)
	default:
		return "", false, false
	}

	// rest は "<YYYY-MM-DDThh-mm-ss>-<id...>"。タイムスタンプ部は固定長 19 文字。
	const tsLen = 19
	if len(rest) < tsLen+2 || rest[tsLen] != '-' {
		return "", false, false
	}
	id = rest[tsLen+1:]
	if id == "" {
		return "", false, false
	}
	return id, compressed, true
}
