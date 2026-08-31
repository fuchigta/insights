# セキュリティ上の問題を見つけたら

**公開の Issue には書かないでください。** GitHub のプライベート脆弱性報告を有効にしています。
このリポジトリの [Security タブ](https://github.com/fuchigta/insights/security) から
「Report a vulnerability」で非公開に報告できます。

## このツールが触るもの

insights は利用者のローカルにあるコーディングエージェントのセッションログを読み、SQLite に
取り込んで AI に評価させます。したがって次のものが関係します。

- **セッション本文**（会話・ツール呼び出し・成果物の痕跡）。業務内容や認証情報が含まれうる
- **評価のために外部へ送る内容**。`claude -p` に渡すのはセッションを要約した台本で、
  `internal/judge/prompt.go` が組み立てる
- **ローカルの DB とレポート**（既定で `~/.insights` 配下）

取り込み対象から除外する設定（`exclude.projects` / `exclude.entrypoints`）については
[docs/configuration.md](../docs/configuration.md)、送信内容の考え方は
[docs/privacy.md](../docs/privacy.md) を参照してください。

## 対応

個人が趣味で開発しているツールなので、SLA はありません。報告は確認しますが、返答までに
時間がかかることがあります。
