# スキルの設置（`spotter skills`）

コーディングエージェント（Claude Code / Codex CLI など）向けの「スキル」（`SKILL.md`
を中心としたファイル一式。[Agent Skills](https://agentskills.io/specification)
というオープン標準の形式）を、バイナリに同梱して配布・設置するコマンド群です。

```
spotter skills list [--json]
spotter skills show <name> [--file <path>] [--list]
spotter skills install <target> [--scope project|user] [--dir <path>] [--only <names>] [--force] [--dry-run]
spotter skills uninstall <target> [--scope project|user] [--dir <path>] [--only <names>] [--force] [--dry-run]
spotter skills status [--scope project|user]
```

## ターゲット

同梱スキルは `SKILL.md` を中心とした同一のファイル一式で、配置先ディレクトリだけが
エージェントによって異なります。

| ターゲット | エイリアス | project の配置先 | user の配置先 |
|---|---|---|---|
| `claude` | `claude-code` | `.claude/skills/<name>/` | `~/.claude/skills/<name>/`（`CLAUDE_CONFIG_DIR` があればそちら） |
| `agents` | `codex`, `gemini`, `cursor`, `copilot` | `.agents/skills/<name>/` | `~/.agents/skills/<name>/` |

`agents` は Codex CLI・Gemini CLI・Cursor・GitHub Copilot などが共通で読む
`.agents/skills/` パスへの窓口です（Agent Skills 標準がこの1パスに収束している
ため、エージェントごとに個別のターゲットを持たせていません）。`target` に `all` を
指定すると両方に設置・削除します。

## スコープ

`--scope project`（既定）はリポジトリ直下、`--scope user` はホームディレクトリ配下
に設置します。`--dir <path>` を指定すると `--scope` を無視してそのディレクトリへ
直接設置します（`target` に `all` を指定しているときは使えません。1 ターゲットに
つき出力先は 1 つのため）。

## 冪等性・所有権

`spotter skills install` は `SKILL.md` の frontmatter の `metadata` に
`managed-by: spotter` / `spotter-version` / `spotter-content-hash` を埋め込み、
これを設置先ディレクトリの所有権とバージョンの目印にします。

- 設置先ディレクトリが無ければ新規作成します
- 既にあり、`managed-by: spotter` が付いていて、内容のハッシュとバージョンが
  最新と一致していれば何もしません（`already` として報告するだけ）
- 既にあり、`managed-by: spotter` が付いているがハッシュかバージョンが異なれば、
  ディレクトリを丸ごと置き換えます（`updated`）。spotter 管理下のディレクトリは
  ローカルな手直しの有無を区別せず常に最新化します。手を入れたい内容は
  `.claude/skills/` 等の外に自分のスキルとして置いてください
- 既にあり、`managed-by: spotter` が付いていない（＝ spotter が設置したもの
  ではない）場合はエラーになります。`--force` で上書きできます

**この判定が見るのは `SKILL.md` 1 枚の frontmatter だけです。** `references/` 配下の
ファイルを個別に削除・改変しても、`SKILL.md` 自体に手を加えていなければ検知されず
`already`／`spotter skills status` の `最新` 判定はそのままになります。設置先の中身が
壊れていそうなときは、判定に頼らず `spotter skills install --force` で明示的に
作り直してください。

`spotter skills uninstall` も同様に、`managed-by: spotter` が付いたディレクトリ
だけを削除します。付いていないディレクトリの削除には `--force` が必要です。

## `--dry-run`: 何もせず計画だけ見る

```bash
spotter skills install claude --dry-run
```

書き込みは一切行わず、どのターゲットのどのディレクトリへ、どのスキルを設置する
予定かだけを表示します。

## 設置状況の確認

```
spotter skills status [--scope project|user]
spotter doctor
```

`spotter skills status` は同梱スキル全部について、対象範囲（既定 project）での
設置状況（未設置 / spotter 管理外 / 最新 / 更新あり）を一覧表示します。
`spotter doctor` は project スコープに限定して、実際に設置されているスキルだけを
要約表示します（`--scope` は持ちません。user スコープは環境依存のため、このリポジトリ
の状態を見る `doctor` の対象外です）。

## `--only`: 対象スキルを絞る

```bash
spotter skills install claude --only spotter-docs
```

カンマ区切りで対象スキルを絞れます。省略時は同梱スキル全部が対象です。

## Claude Code プラグインとしての配布

`spotter` バイナリを介さず、Claude Code のプラグイン機構だけでスキルを入れたい場合は、
このリポジトリ自体を Claude Code のマーケットプレイスとして追加できます
（`.claude-plugin/marketplace.json` / `.claude-plugin/plugin.json` を同梱しています）。

```
/plugin marketplace add fuchigta/spotter
/plugin install spotter@spotter
```

この経路は `skills/` 配下のスキルをそのまま Claude Code に見せます。各スキルのソース
自体に `metadata.managed-by: spotter` を書いているのでそれは残りますが、
`spotter skills install` が追加で埋め込む `spotter-version` / `spotter-content-hash`
（バージョン整合の確認に使う）は付与されません。つまりこの経路でインストールした
スキルは「spotter が作ったもの」とは分かっても「今のバイナリと同じバージョンか」は
`spotter skills status` では確認できません。Codex など `.agents/skills/` を読む他の
エージェント向けには使えません（プラグインのスキルはプラグインキャッシュ内の
`skills/` から読まれ、`.agents/skills/` には配置されないため）。両方に配りたい場合は
`spotter skills install all` を使ってください。

`spotter-docs` スキルはこの経路だと `references/` が実体化されません
（`spotter skills install` 時に spotter バイナリが `docs/` から合成する仕組みのため）。
`spotter-docs` の SKILL.md はこのケースに対応するフォールバック（`docs/` を直接探す）を
案内していますが、
確実に `references/` 込みで使いたい場合は次を使ってください。

```
spotter skills install claude --only spotter-docs
```

