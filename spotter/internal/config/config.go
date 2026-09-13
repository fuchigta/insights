// Package config は spotter の設定ファイル（既定 .spotter.yml）を読み込む。
//
// 組み込み検査（doc-sync など）のオプションは Go の構造体タグによるデコードのみで
// 検証する（docs/hooks-extraction.md の「組み込み type のオプション検証は schema を
// 経由しない」を参照）。`command` を持つ type（外部コマンド検査）のオプションは
// CheckConfig.Options に集約され、internal/schema での検証を経て検査コマンドに渡る。
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
	// Types は組み込み type の default 上書き、または command 型（外部コマンド検査）の
	// 登録に使う。組み込み type は types に書かなくても checks から使える。
	Types map[string]TypeConfig `yaml:"types,omitempty"`
	// RequiredVersion は spotter バイナリの下限バージョン（例: "v0.3.0"）。
	// 手元のバイナリがこれを満たさない場合、spotter は検査を実行せずエラーにする
	// （internal/version.Satisfies を参照。docs/hooks-extraction.md §5）。
	RequiredVersion string `yaml:"required_version,omitempty"`
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

	// Options は command 型（外部コマンド検査）向け。上記のどの組み込みフィールド名にも
	// 一致しない残りのキーがここに集まる（yaml.v3 の inline map）。types.<type>.schema
	// で検証してから検査コマンドに渡す。
	Options map[string]any `yaml:",inline"`
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

// TypeConfig は types.<name> の内容。組み込み type と同名なら「default の上書き」、
// それ以外の名前なら command を必須とする「新しい type の登録」のどちらかを表す
// （両方の意味を同時には持てない。config.Load で検証する）。
type TypeConfig struct {
	// Command が設定されていれば、この type は外部コマンドで実装される。
	Command string `yaml:"command,omitempty"`
	// Transport は checks 側の Options を検査コマンドへどう渡すか（file | args | env）。
	// 省略時は file。command が無い（組み込み type の default 上書き）場合は無意味。
	Transport string `yaml:"transport,omitempty"`
	// Schema は checks 側で渡せる Options の形（省略可）。command が無い場合は無意味。
	Schema *SchemaConfig `yaml:"schema,omitempty"`
	// Default はこの type のインスタンス全体に効く既定値。
	Default *TypeDefault `yaml:"default,omitempty"`
}

// TypeDefault は types.<name>.default。
type TypeDefault struct {
	// Granularity は command 型の起動粒度（squashed | per-commit）。checks 側からは
	// 上書きできない。組み込み type の default 上書きでは無意味（組み込みは Go 側で固定）。
	Granularity string `yaml:"granularity,omitempty"`
	// Exempt は免除設定の既定値。checks.<key>.exempt がこれを上書きする。
	Exempt *ExemptConfig `yaml:"exempt,omitempty"`
}

// SchemaConfig は command 型検査に渡せる Options の形。simple と json-schema は
// どちらか一方だけを指定する（同時指定はエラー）。
type SchemaConfig struct {
	// Simple はフィールド名 → 型・必須かどうかの簡易記法。
	Simple map[string]FieldSpec `yaml:"simple,omitempty"`
	// JSONSchema はフル JSON Schema（type / pattern / enum などの制約を含む）。
	JSONSchema map[string]any `yaml:"json-schema,omitempty"`
}

// FieldSpec は SchemaConfig.Simple の 1 フィールド分。
type FieldSpec struct {
	// Type は "string" / "integer" / "number" / "boolean" / "array" のいずれか。
	Type string `yaml:"type"`
	// Items は Type が "array" のときの要素の型（スカラーのみ）。
	Items string `yaml:"items,omitempty"`
	// Required はこのフィールドを必須にするか。
	Required bool `yaml:"required,omitempty"`
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

// builtinTypes は組み込み type の一覧。
var builtinTypes = map[string]bool{
	TypeDocSync:       true,
	TypeUnwantedFiles: true,
	TypeDocPaths:      true,
	TypeCommitSubject: true,
	TypeConsistency:   true,
}

// IsBuiltinType は name が組み込み type かどうかを返す。
func IsBuiltinType(name string) bool {
	return builtinTypes[name]
}

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

	for name, tc := range cfg.Types {
		if err := validateTypeConfig(name, tc); err != nil {
			return nil, fmt.Errorf("config: types.%s: %w", name, err)
		}
	}

	for key, cc := range cfg.Checks {
		if cc.Type == "" {
			return nil, fmt.Errorf("config: checks.%s に type がありません", key)
		}
		if IsBuiltinType(cc.Type) {
			continue
		}
		if tc, ok := cfg.Types[cc.Type]; !ok || tc.Command == "" {
			return nil, fmt.Errorf("config: checks.%s の type %q は未対応です（types.%s に command を登録してください）", key, cc.Type, cc.Type)
		}
	}

	return &cfg, nil
}

// validateTypeConfig は types.<name> 単体の妥当性（組み込みとの衝突、command 型の
// 必須項目）を検証する。checks 側の Options が schema を満たすかは、各検査インスタンスの
// 構築時（internal/check/command）に個別に検証する。
func validateTypeConfig(name string, tc TypeConfig) error {
	if IsBuiltinType(name) {
		if tc.Command != "" {
			return fmt.Errorf("組み込み type と同名です。command を指定して差し替えることはできません（衝突。typo でなければ別名にしてください）")
		}
		if tc.Schema != nil || tc.Transport != "" {
			return fmt.Errorf("組み込み type の default 上書きでは schema/transport は指定できません")
		}
		if tc.Default != nil && tc.Default.Granularity != "" {
			return fmt.Errorf("組み込み type の granularity は上書きできません")
		}
		return nil
	}

	if tc.Command == "" {
		return fmt.Errorf("command が必要です（組み込み type の default 上書きでなければ）")
	}
	switch tc.Transport {
	case "", "file", "args", "env":
	default:
		return fmt.Errorf("transport %q は未対応です（file | args | env）", tc.Transport)
	}
	if tc.Schema != nil && len(tc.Schema.Simple) > 0 && len(tc.Schema.JSONSchema) > 0 {
		return fmt.Errorf("schema には simple と json-schema のどちらか一方だけを指定してください")
	}
	if tc.Default == nil || tc.Default.Granularity == "" {
		return fmt.Errorf("default.granularity が必要です（squashed | per-commit）")
	}
	switch tc.Default.Granularity {
	case "squashed", "per-commit":
	default:
		return fmt.Errorf("default.granularity %q は未対応です（squashed | per-commit）", tc.Default.Granularity)
	}
	return nil
}

// ResolveExempt は checks.<key>.exempt を、types.<type>.default.exempt →
// システム既定の順にフォールバックさせる。トレーラ名の既定値は type ではなく
// checks のキーから生成する（同じ type を複数インスタンス化したときにトレーラ名が
// 衝突しないようにするため）。
func (cfg *Config) ResolveExempt(key string, cc CheckConfig) (enable bool, trailer string) {
	enable = defaultExemptEnable(cc.Type)
	trailer = defaultTrailer(key)

	if tc, ok := cfg.Types[cc.Type]; ok && tc.Default != nil && tc.Default.Exempt != nil {
		if tc.Default.Exempt.Enable != nil {
			enable = *tc.Default.Exempt.Enable
		}
		if tc.Default.Exempt.Trailer != "" {
			trailer = tc.Default.Exempt.Trailer
		}
	}

	if cc.Exempt != nil {
		if cc.Exempt.Enable != nil {
			enable = *cc.Exempt.Enable
		}
		if cc.Exempt.Trailer != "" {
			trailer = cc.Exempt.Trailer
		}
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
