---
name: insights
description: AIコーディングエージェント（Claude Codeなど）のセッションログを日次集計し、AI評価に基づく振り返りと改善提案を出すinsights CLIの使い方。「今週いくら使った」「どのモデルに金がかかっている」「先週の振り返りは」「改善提案は実行できているか」「昨日は何をしたか」「セッションのコストは」など、AI利用のコスト・時間・成果・改善提案の進捗を尋ねられたときに参照する。
x-insights-version: "1"
---

# insights

`insights` は Claude Code などのセッションログを日次で取り込んで SQLite に正規化し、
AI (LLM-as-judge) が各セッションを定性評価した上で、日報・振り返り・改善提案を生成する CLI。
このスキルは、insights を使ってユーザーの質問に答えるための使い方をまとめたもの。

## コマンド

すべて `--json` を付けて機械可読な出力を受け取ること。人間向けの表・見出し・強調などを
パースしようとしないこと。

| コマンド | 得られるもの |
|---|---|
| `insights config init` | `~/.insights/config.yaml` を生成する |
| `insights config doctor --json` | 設定値と依存（DB, `claude` CLI 等）の疎通確認 |
| `insights ingest [--since DATE\|--all] --json` | ログを SQLite に取り込む（評価はしない。課金なし） |
| `insights judge [--date DATE] [--force]` | 未評価セッションを AI で評価する（**課金発生**） |
| `insights daily [--date DATE] --json` | 指定日の日報＋振り返りを生成し、内容を JSON で返す |
| `insights report --from DATE --to DATE [--out FILE]` | 任意期間の HTML レポートを生成する |
| `insights run [--date DATE]` | ingest → judge → daily を一括実行する（**課金発生**） |
| `insights actions list --json` / `insights actions show ID --json` | 改善アクションの一覧・詳細（状態を含む） |
| `insights skill install\|status\|uninstall` | このスキル自体の導入・状態確認・削除 |

## レポートが存在しない日を聞かれたら

日次レポートの既定の置き場は `~/.insights/reports/daily/YYYY-MM-DD.md`（日報）と
`~/.insights/reports/retro/YYYY-MM-DD.md`（振り返り）。該当日のファイルがなければ
`insights run --date <date>` で生成できる。

**`run` と `judge` は AI 評価を伴い課金が発生する。実行前に必ずユーザーに確認すること。**
複数日にまたがる ingest（`--all` や広い `--since`）→ judge も同様に確認すること。

## 生ログではなく評価済みのサマリを見る

`~/.claude/projects` 配下の JSONL を直接読みに行かないこと。理由:

- ファイルが大きく、全部読むとコンテキストを圧迫する
- サイドチェーンや再送などがあり、素朴に読むと多重計上しやすい
- 単価計算・定性評価は insights 側で既に済んでいる

質問には `insights daily --json` や `insights actions list --json` など、評価済みの
サマリコマンドで答えること。

## 日報と振り返りは別物

- **日報**（`reports/daily/`）: その日 何を成し遂げたか の記録
- **振り返り**（`reports/retro/`）: 金と時間がどこに消えたか、やり方をどう改善するか

「今日/昨日は何をしたか」には日報を、「コストは」「改善提案は進んでいるか」には振り返りを
読むこと。両方の先頭に再集計可能な YAML フロントマター（日付・セッション数・所要時間・
コスト・モデル別内訳・評価軸ごとの分布など）が付いており、複数日をまたぐ集計はここから
復元できる。本文の Markdown をパースする必要はない。

## 数値を読むときの注意

- LLM による定性評価（outcome / model_fit / ownership 等）は傾向を見るための道具であり、
  絶対値として過度に信頼しないこと
- 単価が未登録のモデルがあると、そのモデル分のコストは過小評価される
  （`unpriced_events` / `unknown_models` を確認する）
