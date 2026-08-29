[← README に戻る](../README.md)

# 定期実行の設定

## ログは約30日で消える（詳しい説明）

**Claude Code のトランスクリプト（`~/.claude/projects/**/*.jsonl`）は約30日で自動削除されます。**
取り込まないまま放置すると、振り返りの母集団が常に直近30日分に制限されてしまい、それより前の履歴は
二度と復元できません。

**`insights run` を日次で回すことが、この消失に対する唯一の防衛線です。** 手動での不定期な `ingest` に
頼らず、下記の手順で必ずスケジュール登録してください。

`insights config doctor` はこのリスクを能動的に警告します。ログディレクトリ配下の jsonl のうち
最も古いファイルの更新日時から経過日数を計算し、**25日を超えていれば**（30日の削除猶予に対して余裕を
持たせた閾値です）次のように警告します。

```
Claude Code ログ:
  projects ディレクトリ: C:\Users\you\.claude\projects
  jsonl 件数: 42
  最古のファイル: 2026-08-01T09:00:00+09:00 (28 日前)
  警告: 最古のログが25日以上前です。Claude Code のログは約30日で自動削除されるため、
        取りこぼす前に `insights ingest` を実行してください。
```

## スケジュール登録

ログの自動削除に対抗するため、`insights run` を毎日 1 回はスケジュール実行してください。
どちらの方法でも、**失敗に気づけるようログの出力先を必ず指定**してください（`run` は失敗時に非ゼロで
終了します）。

### Windows: タスクスケジューラ

`schtasks` で登録する例です（毎日 07:00 に実行、`insights.exe` はビルド済みバイナリのパスに置き換えて
ください）。

```powershell
schtasks /Create /TN "insights-run" /SC DAILY /ST 07:00 `
  /TR "cmd /c C:\path\to\insights.exe run --yes --json >> C:\Users\you\.insights\run.log 2>&1" `
  /RL LIMITED
```

GUI から登録する場合は「操作」で以下を設定します。

- プログラム/スクリプト: `cmd.exe`
- 引数の追加: `/c C:\path\to\insights.exe run --yes --json >> C:\Users\you\.insights\run.log 2>&1`
- トリガー: 毎日、任意の時刻

登録後は次のコマンドでログ末尾を確認できます。

```powershell
Get-Content C:\Users\you\.insights\run.log -Tail 50
```

### macOS / Linux: cron

crontab に 1 行追加します（毎日 07:00 に実行、標準出力・標準エラーをログファイルへ）。

```cron
0 7 * * * /usr/local/bin/insights run --yes --json >> $HOME/.insights/run.log 2>&1
```

`crontab -e` で編集し、`crontab -l` で登録内容を確認してください。ログは `tail -f ~/.insights/run.log`
で追えます。

どちらの環境でも、`insights run` が失敗したら（jsonl の取り込み失敗、`claude` 実行エラーなど）ログに
エラーメッセージが残ります。定期的にログを確認するか、監視ツールから run.log の更新の有無・終了コード
を見張ることを勧めます。

[← README に戻る](../README.md)
