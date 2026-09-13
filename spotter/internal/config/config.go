// Package config は spotter の設定ファイル（既定 .spotter.yml）を読み込む。
//
// docs/hooks-extraction.md にある `types` / `schema` / `command` / `transport` は
// 外部コマンド検査向けの機構で、組み込み検査（doc-sync, unwanted-files）だけを
// 動かす現段階では未実装。組み込み検査のオプションは Go の構造体タグによる
// デコードのみで検証する方針（同ドキュメントの「組み込み type のオプション検証は
// schema を経由しない」を参照）。
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath は設定ファイルの既定の場所。
const DefaultPath = ".spotter.yml"

// Config はリポジトリ直下の設定ファイル全体。
type Config struct {
	Checks map[string]CheckConfig `yaml:"checks"`
}

// CheckConfig は 1 つの検査インスタンスの設定。type によって解釈するフィールドが変わる。
type CheckConfig struct {
	Type   string        `yaml:"type"`
	Exempt *ExemptConfig `yaml:"exempt,omitempty"`

	// doc-sync 用。
	Pairs   []DocSyncPair `yaml:"pairs,omitempty"`
	Exclude []string      `yaml:"exclude,omitempty"`

	// unwanted-files 用。
	MaxBytes int64               `yaml:"max_bytes,omitempty"`
	Deny     []UnwantedFilesDeny `yaml:"deny,omitempty"`

	// doc-paths 用。省略時は README.md / CLAUDE.md / docs/*.md / .github/*.md。
	Docs   []string `yaml:"docs,omitempty"`
	Ignore []string `yaml:"ignore,omitempty"`

	// commit-subject 用。
	AllowedTypes []string `yaml:"allowed_types,omitempty"`

	// consistency 用。
	Sources []ConsistencySource `yaml:"sources,omitempty"`
}

// ExemptConfig は checks.<key>.exempt。フィールド単位でシステム既定にフォールバックするため
// Enable はポインタで「未指定」を表現する。
type ExemptConfig struct {
	Enable  *bool  `yaml:"enable,omitempty"`
	Trailer string `yaml:"trailer,omitempty"`
}

// DocSyncPair は doc-sync の対応表 1 行分。
type DocSyncPair struct {
	Paths string `yaml:"paths"`
	Doc   string `yaml:"doc"`
	When  string `yaml:"when,omitempty"`
}

// UnwantedFilesDeny は unwanted-files の拒否ルール 1 件分。
type UnwantedFilesDeny struct {
	Paths  string `yaml:"paths"`
	Reason string `yaml:"reason"`
}

// ConsistencySource は consistency 検査が 1 つのファイルから集合を抜き出す方法。
//
//   - Line にマッチした行だけを対象にする（省略時は全行）
//   - その行に Extract（キャプチャグループ 1 つ必須）を当て、一致した全てを集める
//   - Split を指定すると、キャプチャした文字列をさらにその区切り文字で分割する
//     （例: "feat|fix|perf" を 1 つずつの要素にする）
type ConsistencySource struct {
	File    string `yaml:"file"`
	Line    string `yaml:"line,omitempty"`
	Extract string `yaml:"extract"`
	Split   string `yaml:"split,omitempty"`
}

// 組み込み type の一覧と、範囲モードでの起動粒度（checks 側からは上書きできない）。
const (
	TypeDocSync       = "doc-sync"
	TypeUnwantedFiles = "unwanted-files"
	TypeDocPaths      = "doc-paths"
	TypeCommitSubject = "commit-subject"
	TypeConsistency   = "consistency"
)

// Load は path から設定を読み込み、最低限の妥当性を検証する。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %s の読み込みに失敗しました: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: %s の解析に失敗しました: %w", path, err)
	}

	for key, cc := range cfg.Checks {
		switch cc.Type {
		case TypeDocSync, TypeUnwantedFiles, TypeDocPaths, TypeCommitSubject, TypeConsistency:
		case "":
			return nil, fmt.Errorf("config: checks.%s に type がありません", key)
		default:
			return nil, fmt.Errorf("config: checks.%s の type %q は未対応です（command 型の外部検査は未実装）", key, cc.Type)
		}
	}

	return &cfg, nil
}

// ResolveExempt は checks.<key>.exempt をシステム既定でフォールバックさせる。
// トレーラ名の既定値は type ではなく checks のキーから生成する（同じ type を複数
// インスタンス化したときにトレーラ名が衝突しないようにするため）。
func ResolveExempt(key string, cc CheckConfig) (enable bool, trailer string) {
	enable = defaultExemptEnable(cc.Type)
	if cc.Exempt != nil && cc.Exempt.Enable != nil {
		enable = *cc.Exempt.Enable
	}

	trailer = defaultTrailer(key)
	if cc.Exempt != nil && cc.Exempt.Trailer != "" {
		trailer = cc.Exempt.Trailer
	}

	return enable, trailer
}

// defaultExemptEnable は type ごとの免除の既定値。commit-subject はメッセージの体裁
// そのものを検証する検査なので、既定で免除を不可にする（CLAUDE.md 参照）。
func defaultExemptEnable(checkType string) bool {
	return checkType != TypeCommitSubject
}

// defaultTrailer は "doc-sync-frontend" のようなキーを "Doc-Sync-Frontend" に変換する。
func defaultTrailer(key string) string {
	parts := strings.Split(key, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "-")
}
