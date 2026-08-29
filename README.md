# insights

Claude Code などのコーディングエージェントの利用を、**PR 数やコミット数のような「動いた量」ではなく、
どんな価値に結びついたか（アウトカム）で振り返るための CLI** です。

セッションログを日次で SQLite に集約し、`claude -p` で各セッションを定性評価し、日報と振り返りを生成し、
改善提案を出し、次回その提案が実行されたかを検証する——このループを閉じることが目的です。

対応ログソースは今のところ Claude Code のみです。既存のログを後から読むだけで完結します。

## インストール

Go 1.25 以降が必要です（`go.mod` の `go 1.25.6` に合わせています）。

```bash
# リポジトリ直下でビルドする場合
go build -o insights ./cmd/insights

# もしくは PATH の通ったディレクトリへ直接インストール
go install github.com/fuchigta/insights/cmd/insights@latest
```

## 最初の一歩

```bash
# 1. 設定ファイルの雛形を書き出す（既定: ~/.insights/config.yaml）
insights config init

# 2. 依存関係とログの状態を診断する（claude/git/gh/glab の疎通、ログの取りこぼしリスクを確認）
insights config doctor

# 3. 既存ログを全件取り込む（初回のみ --all。以降は差分取り込みで十分）
insights ingest --all

# 4. 取り込み・評価・日報生成を一括実行する（課金が発生します。詳細は下記の注意を参照）
#    対象件数と推定コストを表示して確認を求めます。
insights run
```

cron やタスクスケジューラから実行する場合は、確認を省略する `--yes` が必要です
（標準入力が端末でないときは `--yes` が無いと実行されません）。

## コマンド一覧

| コマンド | 説明 |
|---|---|
| `config init` \| `doctor` | 設定ファイルの雛形作成 \| 設定・依存コマンド・ログ状態の診断 |
| `ingest` | セッションログを取り込み DB に反映する（課金なし） |
| `judge` | 未評価セッションを AI で評価する（課金発生） |
| `daily` | 指定日の日報＋振り返りを生成する（課金発生） |
| `report` | 任意期間の日次ロールアップを 1 つの HTML にまとめる（課金なし） |
| `run` | `ingest` → `judge` → `daily` を一括実行する。定期実行向け（課金発生） |
| `actions list` \| `show ID` | 振り返りが生成した改善提案の状態を確認する |
| `skill install` \| `status` \| `uninstall` | 他のコーディングエージェント向けにスキルを導入・確認・削除する |

各コマンドの詳細フラグは `insights <command> --help`、または [docs/configuration.md](docs/configuration.md) を参照してください。

## 生成物のありか

- 日報: `~/.insights/reports/daily/YYYY-MM-DD.md`
- 振り返り: `~/.insights/reports/retro/YYYY-MM-DD.md`
- 任意期間の HTML レポート: `~/.insights/reports/insights-<from>_<to>.html`（`insights report --out` で変更可）

詳しい構成や評価軸の説明は [docs/reports.md](docs/reports.md) を参照してください。

## 必ず知っておくべき注意

- **Claude Code のログは約30日で自動削除されます。** 日次で `insights run` を回さないと、振り返りの
  母集団が失われ、二度と復元できません。定期実行の設定は [docs/scheduling.md](docs/scheduling.md) を参照してください。
- **`insights judge` / `daily` / `run` は課金が発生します。** これらは内部で `claude -p` を呼びますが、
  `claude -p` は Claude Code のサブスクリプション枠ではなく **API の従量枠**を消費します。詳細は
  [docs/cost.md](docs/cost.md) を参照してください。

## ドキュメント

- [docs/configuration.md](docs/configuration.md) — 設定ファイル（`~/.insights/config.yaml`）の全項目と、他のコーディングエージェントからの利用方法
- [docs/scheduling.md](docs/scheduling.md) — 定期実行の設定（Windows タスクスケジューラ / cron）とログ消失の詳細
- [docs/reports.md](docs/reports.md) — 日報・振り返りの構成、評価軸 5 つの詳しい説明、丸めの仕様
- [docs/cost.md](docs/cost.md) — コストの見積もり方、キャッシュ課金の仕組み、Claude Code 表示との数字の食い違い
- [docs/development.md](docs/development.md) — 設計と拡張点、CI、既知の制限（開発者向け）
