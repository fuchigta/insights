package skill

import (
	"fmt"
	"sort"
	"sync"
)

// 【設計メモ: 自己登録方式にしている理由】
// internal/skill/claudecode のような具象 Installer 実装は、Scope/State/Status/
// Result といった型を使うために本パッケージ（skill）を import する必要がある。
// もしこのファイルが Installers() の実装として claudecode を直接 import すると、
// skill → claudecode → skill という import cycle になりビルドできなくなる
// （Go の import cycle 検出はディレクトリの親子関係とは無関係に働くため、
// サブディレクトリだからといって特別扱いはされない）。
//
// そのため本パッケージは具象実装パッケージを一切 import しない。代わりに、
// 各実装パッケージが自身の init() から Register() を呼んで名乗り出る
// 自己登録方式を採る（database/sql.Register や image.RegisterFormat と同じ仕組み）。
// 呼び出し元（将来の cmd/insights・internal/cli）が該当パッケージを
// `import _ "github.com/fuchigta/insights/internal/skill/claudecode"` することで
// 登録が有効になる。
//
// 【将来 codex を追加する場合】
// internal/skill/codex のような新しいパッケージを作り、その init() で
// skill.Register(codex.New()) を呼ぶだけでよい。本ファイルの変更は不要。

var (
	registryMu sync.Mutex
	registry   = map[string]Installer{}
)

// Register は Installer を Agent() 名で登録する。各エージェント実装パッケージが
// 自身の init() から呼び出すことを想定している。同じ Agent() 名で複数回呼ばれた場合は
// 後勝ちで上書きする。i が nil の場合は何もしない。
func Register(i Installer) {
	if i == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[i.Agent()] = i
}

// Installers は登録済みの Installer を Agent() 名の昇順で返す。将来 codex を
// 追加する受け皿。実際に何が含まれるかは、実行バイナリがどのエージェント実装
// パッケージを import しているかに依存する（上記コメント参照）。
func Installers() []Installer {
	registryMu.Lock()
	defer registryMu.Unlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Installer, 0, len(names))
	for _, name := range names {
		out = append(out, registry[name])
	}
	return out
}

// ByAgent は Agent() 名で Installer を引く。見つからなければエラー。
func ByAgent(name string) (Installer, error) {
	registryMu.Lock()
	i, ok := registry[name]
	registryMu.Unlock()

	if !ok {
		return nil, fmt.Errorf("未知のエージェントです: %q", name)
	}
	return i, nil
}
