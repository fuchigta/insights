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
- `commit message`: Conventional Commits の検証（`scripts/check-commit-subject.sh`）
- `repo guards`: 品質のドリフトを止める検査。ローカルの `commit-msg` フックと同じスクリプトを
  同じ引数で呼ぶので、手元で通ったものは CI でも通る
  - `scripts/check-doc-sync.sh`: コードとドキュメントの対応（対応表 `scripts/doc-sync.tsv`）。
    片方だけが入っているコミットがあると落ちる。逃げ道は `Doc-Sync: skip <理由>`
  - `scripts/check-doc-paths.sh`: ドキュメント（`README.md` / `CLAUDE.md` / `docs/*.md` /
    `.github/*.md`）が名指ししているパスの実在。まだ無いものを例として挙げている参照は
    `scripts/doc-paths-ignore.txt` に理由付きで除外する
  - `scripts/check-unwanted-files.sh`: セッションログ・データベース・巨大ファイルの混入。
    逃げ道は `Unwanted-Files: skip <理由>`
  - `scripts/check-commit-types.sh`: Conventional Commits の type 一覧が `cliff.toml` /
    `scripts/check-commit-subject.sh` / `CLAUDE.md` の 3 箇所で一致しているか。
    1 箇所だけに足すと、通るのにリリースノートで「その他」に落ちる

#### 落ちたときにどう直すか

CI での見方は検査によって違います。**この違いは意図的です。**

- `doc sync` は範囲（PR や push）の**端から端までの差分をひとまとめ**に見ます。
  落ちたら、そのブランチに**ドキュメントを直すコミットを足せば通ります**。逃げ道の
  `Doc-Sync: skip` も、範囲内のどれか 1 つのコミットに書いてあれば効きます。
  コミット単位で判定すると、後から直しても先の違反コミットが違反のまま残り、
  履歴の書き換え（`rebase` と force push）を強いることになるためです。
  Dependabot の PR のように、こちらが履歴を書き換えると以降の自動更新が止まってしまう
  ものもあります
- `check-unwanted-files.sh` は**コミット単位**で見ます。こちらは後から消しても直りません。
  一度コミットされたセッションログや DB は、あとで削除しても履歴に残り続けるからです。
  正しい直し方は「消すコミットを足す」ではなく、**そのコミットを履歴から取り除く**こと
  （`git rebase -i` などでやり直して force push）です。main は force push を禁止して
  いますが、作業ブランチにはこの制限はありません

`claude` CLI は CI ランナーに無いため、AI を実際に呼ぶテストはスキップされ、CI 実行自体で課金が
発生することはありません。

テストはパッケージ単位のものに加えて、**コマンド層を通した統合テスト**（`internal/cli`）があります。
一時ディレクトリに作った偽の `~/.claude` ツリーを入力に、`ingest` → `judge` → `daily` → `report` →
`actions list` を実際の cobra コマンドとして順に実行し、パッケージの継ぎ目（サブエージェントの
親への畳み込み、丸めの結果が描画に反映されること、フロントマターからの再集計）を検証します。
これまで見つかった不具合はほぼすべて継ぎ目にあったためです。評価バックエンドは
`internal/cli/deps.go` の `newJudge` を差し替えてフェイクにするので、`claude` は呼ばれません。

## GitHub 側で有効にしているもの

公開リポジトリなので、有料の Advanced Security 相当がいくつか無料で使えます。ワークフローを
書かずに設定だけで動くものを有効にしています。

- **CodeQL（Default setup）**: `go` と `actions` を対象にしたコードスキャン。ワークフロー
  ファイルは置いていません（GitHub 側の設定で動きます）。`actions` を含めているのは、
  ワークフロー自体の脆弱性（式展開の扱いなど）が自分では気付きにくいためです
- **Secret scanning / Push protection**: 秘密情報を含む push を弾きます（公開リポジトリの既定）
- **Dependabot アラート・セキュリティ更新**: 依存の既知脆弱性の検知と修正 PR
- **Dependabot バージョン更新**: `.github/dependabot.yml`。gomod と github-actions を週 1 回、
  それぞれ 1 本にまとめて更新します。`commit-message.prefix` を指定しているのは、既定の
  メッセージ（`Bump x from 1 to 2`）が Conventional Commits ではなく、`commit message`
  ジョブに落とされるためです
- **プライベート脆弱性報告**: 公開 Issue を経由せずに報告できます（`.github/SECURITY.md`）
- **ルールセット（main の履歴を壊さない）**: force push とブランチ削除を禁止しています。
  PR は必須にしていないので、main への直接 push はこれまでどおりできます。止まるのは
  push 済みの履歴を書き換える操作だけです（管理者にも適用されます）

Dependabot が出す Actions の更新 PR（`uses:` のバージョン変更）が `doc sync` に引っかかると
その PR は永久に通らなくなるため、対応表のワークフロー行は `name:` / `run:` / `if:` が
動いたときだけ発火するよう絞ってあります。

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
- **セッション当時のブランチが消えていると、コミットの絞り込みが粗くなります。** 作業ブランチは
  PR/MR のマージと同時に削除されることが多いため、`<branch>` → `origin/<branch>` → 全 ref（`--all`）
  の順にフォールバックしてコミットを探します。最後まで落ちた場合は同じ時間帯の他ブランチの
  コミットも混ざり得ます（「取れない」より「少し多めに取る」を選んでいます）。
- **`glab` が未インストールの環境では GitLab の成果物（MR/Issue）が取れません。** 成果物収集は
  「あれば使う、無ければスキップする」best-effort 設計で、`git`/`gh`/`glab` いずれも欠けていても
  insights 自体は動作を続けます（`evidence.gh` / `evidence.glab` を `auto` にしておけば自動判定されます）。

[← README に戻る](../README.md)
