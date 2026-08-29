[← README に戻る](../README.md)

# 設定ファイル

`~/.insights/config.yaml`（`insights config init` が書き出す既定値）。

```yaml
language: ja
sources:
  claude-code:
    root: ~/.claude
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
| `judge.backend` | AI 評価バックエンド識別子（現状 `claude-cli` のみ対応） | `claude-cli` |
| `judge.model` | 評価に使うモデル | `claude-sonnet-5` |
| `judge.concurrency` | 評価の並列実行数 | `3` |
| `judge.timeout` | `claude` 1 回の実行タイムアウト | `3m0s`（180秒） |
| `evidence.git` | git commit 収集の有効/無効 | `true` |
| `evidence.gh` | `gh`（GitHub CLI）による PR/Issue 収集。`true`/`false`/`auto`（見つかれば使う） | `auto` |
| `evidence.glab` | `glab`（GitLab CLI）による MR/Issue 収集。同上 | `auto` |
| `evidence.max_body_chars` | 成果物本文を評価入力に含める上限文字数 | `4000` |
| `output.dir` | 日報・振り返り・HTML レポートの出力先ディレクトリ | `~/.insights/reports` |
| `report.rollup.cost_share` | 振り返りのプロジェクト別セッション一覧で、個別に載せず「その他 N 件」に丸める基準のひとつ。その日の総コストに対する割合（0..1）。**この割合と `report.rollup.duration_minutes` の両方を下回るセッションだけが丸められます**（どちらか一方でも上回れば個別掲載） | `0.01`（1%） |
| `report.rollup.duration_minutes` | 上記の丸め基準のもう一方。セッションの所要時間（分） | `10` |
| `database` | SQLite DB のパス | `~/.insights/insights.db` |
| `exclude.projects` | 評価対象から除外するプロジェクトパスの一覧 | `[]` |
| `exclude.entrypoints` | 評価対象から除外する entrypoint（例: 特定の自動実行経路）の一覧 | `[]` |
| `goals.global` | レポート全体で重視する価値の説明文。評価プロンプトに渡り、判定の物差しになる | `""` |
| `goals.projects` | プロジェクトパスごとの重視する価値。一致すれば `goals.global` より優先される | `{}` |
| `pricing.overrides` | モデルごとの単価上書き（`input` / `output` / `cache_write_5m` / `cache_write_1h` / `cache_read`、単位は 1M トークンあたり USD） | `{}` |

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

## 他のコーディングエージェントから使う

```bash
insights skill install --scope user
```

を実行すると、Claude Code のスキルとして `insights` の使い方が導入され（既定は
`~/.claude/skills/insights/SKILL.md`）、Claude Code のセッション内から「今週どこにコストが消えた?」
「先週の振り返りは?」「改善提案は実行できているか?」のように尋ねると、`insights daily --json` や
`insights actions list --json` などを叩いて答えてくれるようになります。

`--scope user`（既定）は `~/.claude/skills/insights/` に、`--scope project` はカレントディレクトリの
`.claude/skills/insights/` に配置します。プロジェクト固有の運用にしたい場合は `project` を使ってください。

`insights skill status` で導入状態（未導入 / 最新 / 旧版 / 手で改変済み）を確認できます。手で SKILL.md を
編集した状態で `insights skill install` を再実行するとエラーになり、上書きするには `--force` が必要です。

[← README に戻る](../README.md)
