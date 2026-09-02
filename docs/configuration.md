[← README に戻る](../README.md)

# 設定ファイル

`~/.insights/config.yaml`（`insights config init` が書き出す既定値）。

```yaml
language: ja
sources:
  claude-code:
    root: ~/.claude
    enabled: true
  codex:
    root: ""
    enabled: true
judge:
  backend: claude-cli
  model: claude-sonnet-5
  concurrency: 3
  timeout: 3m0s
evidence:
  git: true
  gh: auto
  glab: auto
  max_body_chars: 4000
  github_hosts: []
  gitlab_hosts: []
output:
  dir: ~/.insights/reports
report:
  rollup:
    cost_share: 0.01
    duration_minutes: 10
database: ~/.insights/insights.db
exclude:
  projects: []
  entrypoints: []
goals:
  global: ""
  projects: {}
pricing:
  overrides: {}
```

| キー | 意味 | 既定値 |
|---|---|---|
| `language` | レポート生成に使う言語コード | `ja` |
| `sources.claude-code.root` | Claude Code のログ置き場（`projects/` サブディレクトリを見る） | `~/.claude` |
| `sources.claude-code.enabled` | Claude Code ソースの有効/無効 | `true` |
| `sources.codex.root` | Codex のログ置き場（`sessions/` と `archived_sessions/` を見る）。**空にしておくと環境変数 `CODEX_HOME`、無ければ `~/.codex` を実行時に解決する** | `""`（自動判定） |
| `sources.codex.enabled` | Codex ソースの有効/無効 | `true` |
| `judge.backend` | AI 評価バックエンド識別子（`claude-cli` / `codex-cli`） | `claude-cli` |
| `judge.model` | 評価に使うモデル。バックエンドに合わせて書く（`codex-cli` なら `gpt-5.5` など） | `claude-sonnet-5` |
| `judge.concurrency` | 評価の並列実行数 | `3` |
| `judge.timeout` | 評価バックエンド 1 回の実行タイムアウト | `3m0s`（180秒） |
| `evidence.git` | git commit 収集の有効/無効 | `true` |
| `evidence.gh` | `gh`（GitHub CLI）による PR/Issue 収集。`true`/`false`/`auto`（見つかれば使う） | `auto` |
| `evidence.glab` | `glab`（GitLab CLI）による MR/Issue 収集。同上 | `auto` |
| `evidence.max_body_chars` | 成果物本文を評価入力に含める上限文字数 | `4000` |
| `evidence.github_hosts` | GitHub Enterprise Server のホスト名一覧。ここに書いたホストは `gh` で収集する | `[]` |
| `evidence.gitlab_hosts` | セルフホスト GitLab のホスト名一覧。ここに書いたホストは `glab` で収集する | `[]` |
| `output.dir` | 日報・振り返り・HTML レポートの出力先ディレクトリ | `~/.insights/reports` |
| `report.rollup.cost_share` | 振り返りのプロジェクト別セッション一覧で、個別に載せず「その他 N 件」に丸める基準のひとつ。その日の総コストに対する割合（0..1）。**この割合と `report.rollup.duration_minutes` の両方を下回るセッションだけが丸められます**（どちらか一方でも上回れば個別掲載） | `0.01`（1%） |
| `report.rollup.duration_minutes` | 上記の丸め基準のもう一方。セッションの所要時間（分） | `10` |
| `database` | SQLite DB のパス | `~/.insights/insights.db` |
| `exclude.projects` | 除外するプロジェクトパスの一覧。**その配下も対象**。取り込み・評価・レポートのすべてで対象外になる | `[]` |
| `exclude.entrypoints` | 除外する entrypoint（例: 特定の自動実行経路）の一覧。同上 | `[]` |
| `goals.global` | レポート全体で重視する価値の説明文。評価プロンプトに渡り、判定の物差しになる | `""` |
| `goals.projects` | プロジェクトパスごとの重視する価値。一致すれば `goals.global` より優先される | `{}` |
| `pricing.overrides` | モデルごとの単価上書き（`input` / `output` / `cache_write_5m` / `cache_write_1h` / `cache_read`、単位は 1M トークンあたり USD） | `{}` |

## ログソース（Claude Code / Codex）

有効なソースは取り込み時にまとめて走査されます。**ログ置き場が見つからないソースは黙って
飛ばし**（stderr にその旨を出す）、**すべてのソースで見つからないときだけエラー**にします。
Codex ソースは既定で有効なので、Claude Code しか使っていなくても `insights ingest` は
そのまま動きます。逆に「全部見つからない」は設定ミスの可能性が高いので止めます。

いま何が見えているかは `insights config doctor` が表示します（`sessions` ディレクトリの
場所、ロールアウト件数、そのうち圧縮済みの件数）。

Codex 固有の注意:

- **ロールアウトは書き終えて 7 日ほど経つと Codex 自身が zstd（`.jsonl.zst`）に圧縮します。**
  insights は圧縮版もそのまま読むので、取り込みが途切れることはありません
- **Codex のログには自動削除がありません。** Claude Code（約30日）と違い、`ingest --all` で
  後からまとめて取り込めます
- アーカイブしたスレッド（`archived_sessions/`）も取り込みます。片付けただけで、実際に
  行われた作業であることは変わらないためです
- `codex exec` / MCP 経由のセッションは**非対話**として扱います（人が同席せず、実行中に
  軌道修正も検収もできないため）。集計の「対話/自動」の内訳と、評価軸の読み替えに効きます
- サブエージェント・Codex 内部のスレッドはコストを親セッションに畳み込みますが、評価自体は個別に行います

## 除外するパスの書き方

`exclude.projects` の比較では次を吸収します。手で正規化する必要はありません。

- `~` / `~/...` / `$HOME` の展開
- パス区切りの差（`C:\Users\me\src` と `C:/Users/me/src` は同じ）
- 大文字小文字の差、末尾のスラッシュ

そして**書いたディレクトリの配下も除外されます**。作業用の一時ディレクトリのように、
その下に毎回違う名前のディレクトリが切られる場合でも 1 行で済みます。

```yaml
exclude:
  projects:
    - ~/AppData/Local/Temp   # 配下の作業ディレクトリごと落ちる
    - G:/マイドライブ/obsidian
```

境界はパス区切りで判定するので、`~/src/foo` と書いても `~/src/foobar` は除外されません。
Windows のパスを `"..."`（二重引用符）で囲むと YAML が `\` をエスケープとして解釈するため、
引用符なしか `'...'`（単一引用符）で書くか、`/` 区切りにしてください。

## 除外はいつ効くか

`exclude.projects` / `exclude.entrypoints` は、`insights ingest` の取り込み時に弾くだけでなく、
`insights judge` と `insights daily` が DB からセッションを読んだ後にも適用されます。
そのため**すでに取り込んだ後から除外を足しても効きます**（評価もされず、日報にもコストにも
載りません）。

除外しても DB の中身は消えません。設定から外せばまた対象に戻ります。取り込み済みのデータ自体を
消したい場合は、DB（`~/.insights/insights.db`）を直接操作してください。

## セルフホストの GitHub / GitLab

PR/Issue/MR の収集先は `origin` のリモート URL のホスト名から決めます。判定は次の順です。

1. `evidence.github_hosts` / `evidence.gitlab_hosts` に書かれたホストとの一致
2. `github.com` / `gitlab.com`（SaaS）
3. ホスト名のラベルに `github` / `gitlab` を含むかの推測（`gitlab.example.co.jp` や
   `github.corp.example.com` はこれで拾えます）

`git.example.com` のようにホスト名からフォージが分からない場合は 3 でも判定できないので、
設定に明示してください。書かないと「判定できないためスキップ」の警告ログだけが出て、
MR/Issue が収集されません。

```yaml
evidence:
  gitlab_hosts:
    - git.example.co.jp
  github_hosts:
    - ghe.example.com
```

判定できたホストは `GITLAB_HOST` / `GH_HOST` 環境変数として `glab` / `gh` に渡します。
そのため、各 CLI がそのホストに対して認証済みである必要があります
（`glab auth login --hostname git.example.co.jp` / `gh auth login --hostname ghe.example.com`）。

`goals` は評価軸のうち「目的達成度」や「モデル選択の妥当性」を判定する際の物差しとして使われます。
例えば手離れの良さを重視するプロジェクトと、知見の資産化を重視するプロジェクトでは「良い成果」の意味が
異なるため、プロジェクトごとに書き分けてください。

```yaml
goals:
  global: "動くものより、あとで読み返して意思決定に使える記録を残すことを重視する"
  projects:
    "C:\\Users\\you\\src\\github.com\\you\\life-automation": "生活の自動化。手離れの良さを重視"
    "C:\\Users\\you\\src\\github.com\\you\\notes": "知見の資産化。使い捨て調査で終わらせない"
```

`exclude.projects` はメタ作業（insights リポジトリ自体の開発など）を評価対象から外すのに使います。
比較は `~` 展開とパス正規化を行った上での完全一致で、Windows のパス区切り・大文字小文字の違いは
吸収されます。

## 評価バックエンドを切り替える

`judge.backend` で AI 評価をどのエージェントに任せるかを選べます。

```yaml
judge:
  backend: codex-cli
  model: gpt-5.5
```

| バックエンド | 呼ぶコマンド | 構造化出力の渡し方 | 実費の報告 |
|---|---|---|---|
| `claude-cli`（既定） | `claude -p --output-format json` | `--json-schema`（JSON を直接） | あり（`total_cost_usd`） |
| `codex-cli` | `codex exec --json` | `--output-schema`（スキーマ**ファイル**のパス） | なし |

`codex-cli` を選ぶときに知っておくべき差:

- **1 回あたりの支出上限を渡せません。** `claude -p` の `--max-budget-usd` に相当する
  フラグが `codex exec` には無いため、暴走の歯止めは `judge.timeout` と `--limit` だけです
- **実費（USD）を報告しません。** そのため事前確認では金額を出さず、「見積もれない」と
  表示します（$0 という意味ではありません）。詳細は [docs/cost.md](cost.md)
- 評価は `--sandbox read-only --ephemeral` で実行します。読み取りだけに限定し、評価の実行
  自体がセッションログとして残らない（次回の取り込みで自分の統計を汚さない）ようにするためです
- 役割指示（system prompt 相当）を別枠で渡せないため、プロンプト本文の先頭に連結します

## 他のコーディングエージェントから使う

```bash
insights skill install --scope user                 # Claude Code（既定）
insights skill install --agent codex --scope user   # Codex
```

を実行すると、そのエージェントのスキルとして `insights` の使い方が導入され、セッション内から
「今週どこにコストが消えた?」「先週の振り返りは?」「改善提案は実行できているか?」のように
尋ねると、`insights daily --json` や `insights actions list --json` などを叩いて答えて
くれるようになります。

配置先は `--agent` と `--scope` の組で決まります。

| エージェント | `--scope user` | `--scope project` |
|---|---|---|
| `claude-code`（既定） | `~/.claude/skills/insights/` | `./.claude/skills/insights/` |
| `codex` | `$CODEX_HOME/skills/insights/`（既定 `~/.codex/skills/insights/`） | `./.codex/skills/insights/` |

プロジェクト固有の運用にしたい場合は `project` を使ってください。

`insights skill status` で導入状態（未導入 / 最新 / 旧版 / 手で改変済み）を確認できます。手で SKILL.md を
編集した状態で `insights skill install` を再実行するとエラーになり、上書きするには `--force` が必要です。

[← README に戻る](../README.md)
