[← README に戻る](../README.md)

# 設計と拡張点

現時点で対応しているコーディングエージェントは Claude Code のみですが、**将来 Codex などに対応できる
よう、3 つの拡張点が用意されています。**

- `internal/source` の `Source` インターフェース — ログソースの発見（`Discover`）とパース（`Parse`）を
  抽象化しています。
- `internal/judge` の `Judge` インターフェース — AI 評価バックエンドを抽象化しています。プロンプトと
  JSON Schema を渡して JSON を受け取るだけの契約です。`claude-cli` 実装（`internal/judge/claudecli`）は
  `claude -p --output-format json` に `--json-schema` を渡し、claude 側で検証済みの構造化出力を
  `structured_output` フィールドからそのまま受け取ります。これにより、モデルが説明文やコードフェンスを
  混ぜて返してもパースが壊れません（評価モデルが Markdown 混じりの応答を返す逸脱が実際に観測されたため
  導入しました）。`structured_output` が無い古い経路では、応答本文からの JSON 抽出にフォールバックします。
- `internal/skill` の `Installer` インターフェース — スキルの配布先を抽象化しています。各エージェント
  向けの実装パッケージが自身の `init()` で自己登録する方式（`database/sql.Register` と同様）を採っており、
  レジストリ側（`internal/skill/registry.go`）は具象実装を一切 import しません。

**Codex 対応は現時点では実装されていません。** 上記 3 つのインターフェースを満たす実装パッケージ
（`internal/source/codex` など）を追加すれば差し込める設計になっている、という段階です。

## CI

`.github/workflows/ci.yml` が push / PR のたびに次を実行します。

- `test`: Ubuntu / Windows / macOS の 3 OS で `go vet` → `go build ./...` → `go test ./...`
- `lint`: `gofmt -l ./cmd ./internal` による整形チェックと、`go mod tidy` 実行後の `go.mod` / `go.sum` 差分チェック
- `race`: `go test -race ./...`（評価の並行実行とストア書き込みの直列化の境界を競合検出器で確認）

`claude` CLI は CI ランナーに無いため、AI を実際に呼ぶテストはスキップされ、CI 実行自体で課金が
発生することはありません。

テストはパッケージ単位のものに加えて、**コマンド層を通した統合テスト**（`internal/cli`）があります。
一時ディレクトリに作った偽の `~/.claude` ツリーを入力に、`ingest` → `judge` → `daily` → `report` →
`actions list` を実際の cobra コマンドとして順に実行し、パッケージの継ぎ目（サブエージェントの
親への畳み込み、丸めの結果が描画に反映されること、フロントマターからの再集計）を検証します。
これまで見つかった不具合はほぼすべて継ぎ目にあったためです。評価バックエンドは
`internal/cli/deps.go` の `newJudge` を差し替えてフェイクにするので、`claude` は呼ばれません。

## 制限と既知の弱点

- **LLM による評価は傾向を見る道具であり、絶対値ではありません。** `outcome` / `model_fit` / `ownership`
  などの判定はブレを含みます。分布の数字だけを根拠にせず、具体的なセッションの内容と突き合わせて
  読んでください。
- **評価にはブレがあります。** 各評価結果は `prompt_version` と `confidence`（low/medium/high）を保存して
  おり（`session_evals` テーブル、冪等キーは `(session_id, prompt_version)`）、評価プロンプトを更新した
  ときは再評価できます。トランスクリプトの情報が乏しいセッションでは、無理に断定せず低い `confidence`
  が返るのが正しい振る舞いです。
- **取りこぼした過去ログは復元できません。** 約30日で削除される前に取り込めなかったセッションは、
  二度と評価対象にできません。
- **`glab` が未インストールの環境では GitLab の成果物（MR/Issue）が取れません。** 成果物収集は
  「あれば使う、無ければスキップする」best-effort 設計で、`git`/`gh`/`glab` いずれも欠けていても
  insights 自体は動作を続けます（`evidence.gh` / `evidence.glab` を `auto` にしておけば自動判定されます）。

[← README に戻る](../README.md)
