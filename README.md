# insights

Claude Code などのコーディングエージェントの利用を、**PR 数やコミット数のような「動いた量」ではなく、
どんな価値に結びついたか（アウトカム）で振り返るための CLI** です。

セッションログを日次で SQLite に集約し、`claude -p` で各セッションを定性評価し、日報と振り返りを生成し、
改善提案を出し、次回その提案が実行されたかを検証する——このループを閉じることが目的です。

対応ログソースは今のところ Claude Code のみです。OpenTelemetry のような計測の注入は行わず、
既存のログを後から読むだけで完結します。

## 目次

- [インストールと最初の一歩](#インストールと最初の一歩)
- [ログは約30日で消える（最重要の注意）](#ログは約30日で消える最重要の注意)
- [コマンド一覧](#コマンド一覧)
- [定期実行の設定](#定期実行の設定)
- [コストの話](#コストの話)
- [Claude Code 自身の表示と数字が食い違う件](#claude-code-自身の表示と数字が食い違う件)
- [何を評価するのか](#何を評価するのか)
- [生成物](#生成物)
- [設定ファイル](#設定ファイル)
- [他のコーディングエージェントから使う](#他のコーディングエージェントから使う)
- [設計と拡張点](#設計と拡張点)
- [制限と既知の弱点](#制限と既知の弱点)

## インストールと最初の一歩

Go 1.25 以降が必要です（`go.mod` の `go 1.25.6` に合わせています）。

```bash
# リポジトリ直下でビルドする場合
go build -o insights ./cmd/insights

# もしくは PATH の通ったディレクトリへ直接インストール
go install github.com/fuchigta/insights/cmd/insights@latest
```

導入は次の順序で進めてください。**`doctor` を先に叩くことを強く勧めます。** 何が使えて何が使えないか
（`claude` / `git` / `gh` / `glab` の有無、ログの取りこぼしリスク、書き込み権限）が一目で分かります。

```bash
# 1. 設定ファイルの雛形を書き出す（既定: ~/.insights/config.yaml）
insights config init

# 2. 依存関係とログの状態を診断する
insights config doctor

# 3. 既存ログを全件取り込む（初回のみ --all。以降は差分取り込みで十分）
insights ingest --all
```

`insights config doctor` は設定の妥当性チェックに加えて、`claude`（必須）・`git`（必須）・`gh`（任意）・
`glab`（任意）の疎通確認、Claude Code の jsonl ログの件数・最古ファイルの経過日数、出力先ディレクトリと
DB パスの書き込み可否をまとめて報告します。致命的な設定エラー（`Validate()` が非空）がない限り終了コードは
`0` です。

## ログは約30日で消える（最重要の注意）

**Claude Code のトランスクリプト（`~/.claude/projects/**/*.jsonl`）は約30日で自動削除されます。**
取り込まないまま放置すると、振り返りの母集団が常に直近30日分に制限されてしまい、それより前の履歴は
二度と復元できません。

**`insights run` を日次で回すことが、この消失に対する唯一の防衛線です。** 手動での不定期な `ingest` に
頼らず、後述の [定期実行の設定](#定期実行の設定) で必ずスケジュール登録してください。

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

## コマンド一覧

```
insights config init|doctor
insights ingest [--since DATE|--all] [--dry-run] [--no-evidence] [--json]
insights judge  [--date DATE] [--from DATE --to DATE] [--force] [--yes] [--limit N] [--json]
insights daily  [--date DATE] [--no-judge] [--yes] [--json]
insights report --from DATE --to DATE [--out FILE]
insights run    [--date DATE] [--yes] [--json]
insights actions [list|show ID] [--all] [--status STATUS] [--json]
insights skill  install|status|uninstall [--agent NAME] [--scope user|project] [--force]
```

すべてのサブコマンドはルートの永続フラグ `--config`（設定ファイルパス、既定 `~/.insights/config.yaml`）・
`--db`（DB パスの上書き）・`--verbose`（詳細ログ）・`--json`（機械可読出力）を共有します。

| コマンド | 説明 | 主なフラグ |
|---|---|---|
| `config init` | 設定ファイルの雛形を既定パスに書き出す | `--force`（既存ファイルを上書き） |
| `config doctor` | 設定・外部コマンド・ログの状態を診断する。課金なし | なし |
| `ingest` | セッションログを発見して正規化し DB に取り込む。既定では前回取り込み以降の差分のみ、初回は全件。**課金なし** | `--since DATE` / `--all`（同時指定不可） / `--dry-run`（DB に書かない） / `--no-evidence`（git/gh/glab の成果物収集をスキップ） |
| `judge` | 未評価セッションを AI で評価する。**課金発生** | `--date` / `--from`・`--to` / `--force`（再評価） / `--yes`（非対話実行の確認省略） / `--limit N` |
| `daily` | 指定日の日報＋振り返りを生成する。未評価セッションがあれば内部で judge 相当の評価を行い、加えて日報・振り返りの本文生成そのものが AI 呼び出しを 2 回行う。**課金発生** | `--date` / `--no-judge`（未評価セッションの事前評価だけをスキップする。日報・振り返りの生成 AI 呼び出しはスキップされない） / `--yes`（課金確認を省略。非対話環境では必須） |
| `report` | 任意期間の日次ロールアップを束ね、単一の自己完結 HTML ファイルに変換する。AI 呼び出しなし。**課金なし** | `--from DATE`（必須） / `--to DATE`（必須） / `--out FILE`（既定 `<output.dir>/insights-<from>_<to>.html`） |
| `run` | `ingest` → `judge` → `daily` を一括実行する。cron / タスクスケジューラ向け。**課金発生** | `--date` / `--yes` / `--json` |
| `actions list` / `actions show ID` | 振り返りが生成した改善提案の状態を確認する。AI 呼び出しなし | `--all`（全状態） / `--status STATUS`（`open`\|`done`\|`dropped`\|`expired`。既定は `open` のみ） |
| `skill install`\|`status`\|`uninstall` | 他のコーディングエージェント向けにこのツールのスキルを導入・確認・削除する | `--agent NAME`（既定 `claude-code`） / `--scope user\|project`（既定 `user`） / `--force`（install のみ。手で改変されたスキルの上書き） |

**`run`・`judge`・`daily` は非対話環境では `--yes` が必須です。** AI 評価やレポート生成には課金が
発生するため、対話端末以外で確認なしに実行させない設計になっています。`daily` は `--no-judge` を
付けても日報・振り返りの生成そのものが AI（`claude -p`）を呼ぶため、**課金確認は `--no-judge` の
有無に関わらず必ず行われます。** cron・タスクスケジューラから叩く場合は必ず `--yes` を付けてください。

`ingest`・`report`・`actions`・`daily` は `--json` を付けると機械可読な JSON を出力します（人間向けの
表・見出し・強調文字列をパースしようとしないでください）。他のコーディングエージェントのスキルはこの
`--json` 出力を叩いて質問に答えます。

## 定期実行の設定

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

## コストの話

- **利用額はトークン数 × 単価表の推定値です。** Claude Code の jsonl には `costUSD` フィールドが無いため、
  自前でトークン数を集計し `internal/pricing/prices.json`（1M トークンあたりの USD、Anthropic 公開 API
  レート）を掛けて算出しています。契約単価が異なる場合は設定の `pricing.overrides` で上書きできます。
- **`claude -p` は API の従量枠を消費します（対話的な Claude Code のセッションとは別枠です）。**
  `insights judge` / `insights daily` / `insights run` はいずれも内部で `claude -p` を呼びます。対話的に
  使う Claude Code のセッションはサブスクリプションの範囲内で完結しますが、`claude -p` は API 利用額の
  従量課金枠を消費します。**この枠は月あたり限られているため、評価の実行（特に `--force` での再評価や
  広い期間の一括評価）は計画的に行ってください。** お試しで分析させたいだけなら、`claude -p` を都度叩く
  `insights judge` の代わりに、サブスクリプション枠内で完結する Claude Code のサブエージェントに要約を
  依頼するなど、別の手段を検討してください。
- **1 回あたりの支出には必ず上限が掛かります。** `claude -p` の呼び出しには常に `--max-budget-usd` を
  渡しており、既定値は 1 回あたり **$1.0**（`internal/judge/claudecli` の `defaultMaxBudgetUSD`）です。
  暴走した 1 回の評価が枠を食い潰さないための安全装置です。
- **`insights judge` / `insights daily` は実行前にコストを見積もり、確認を求めます。** 見積もりは実測に
  基づく概算で、1 セッションあたり `claude-haiku-4-5` で約 **$0.08**、`claude-sonnet-5` はその 3 倍の約
  **$0.24** を基準にしています（`internal/cli/judge.go` の `estimatedCostPerSessionHaikuUSD` /
  `estimatedCostPerSessionSonnetUSD`）。この基準は「`--json-schema` 付きの最小呼び出しの実測（約
  $0.074。構造化出力がツール経由で実装されているため固定費が乗ります）を下回らないよう安全側に寄せた値」
  であり、スキーマ無しの実セッション評価の実測（約 $0.025）よりも高めに出ます。セッションの長さ・委譲件数
  によって実費は大きく変動するため、あくまで事前確認用の目安です。既定モデルは `claude-sonnet-5` で、
  `judge.model`（設定ファイル）で変更できます。
- **評価コスト自体もレポートに計上されます。** 日次ロールアップの `meta.judge_cost_usd` /
  `meta.judge_session_ids` として記録され、振り返りレポートの生成コストの中に含まれます。これは
  「振り返りのためにかけたコストが、振り返り自体で使い切れていないか」を自己監視するための仕組みです。
  評価の実行自体が作る Claude Code セッション（`~/.insights/judge-workspace` 配下）は、集計対象を
  汚染しないよう `ingest` の時点で自動的に除外されます。
- **単価未登録のモデルがあると、そのモデル分のコストは過小評価されます。** `prices.json` に無いモデル名
  （`non_billable` に列挙された疑似モデル名を除く）を使ったセッションは 0 円として扱われるのではなく、
  「単価不明」として別掲され、日報・振り返りの但し書きと `insights ingest` の警告に必ず出ます。

### キャッシュ課金について

レポートの金額のうち `cache_read` が大半を占めることがありますが、**これは正常であり、無駄ではありません。**
Anthropic の公式ドキュメント（platform.claude.com/docs/en/build-with-claude/prompt-caching）によれば、
未キャッシュの入力を基準に単価は次のとおりです。

- 5 分キャッシュの書き込み（`cache_write_5m`）: 入力の **1.25 倍**
- 1 時間キャッシュの書き込み（`cache_write_1h`）: 入力の **2 倍**
- キャッシュ読み取り（`cache_read`）: 入力の **0.1 倍**

つまり `cache_read` は最も安く文脈を運べている状態で、キャッシュが無ければ同じトークンに最大 10 倍
かかります。**見るべき指標は `cache_reuse_ratio`（`cache_read` ÷ `cache_write`）です。高いほど文脈を
安く再利用できています。低い（`cache_write` が相対的に多い）ときが割高で、同じ文脈が何度も作り直されて
いることを意味します。** この比は日次ロールアップの内部（`Totals.CacheReuseRatio`）で計算され、振り返り
生成のプロンプトに渡って `cost_observation` の所見に反映されます。サイドカー YAML（後述）自体には日合計の
比としては出ませんが、`by_model` の `cache_read_tokens` / `cache_write_tokens` から同じ比を計算できます。

**「セッションを分ける」はコスト削減策になりません。** 新しいセッションを始めるたびに `cache_write`
（1.25〜2 倍）を払い直すことになるため、むしろ高くつきます。長く連続したセッションでキャッシュを
使い回すほうが安上がりです。

## Claude Code 自身の表示と数字が食い違う件

これは実測で確認した事実です。

Claude Code の JSONL トランスクリプトは、1 つのアシスタント応答（1 回の API 応答）を content ブロックごとに
複数行へ分割して書き出し、**各行に同じ `usage` を複写します。** 正しくトークン数を数えるには
`message.id` で重複排除する必要があり、insights の取り込み処理はこれを行っています。

一方、Claude Code 自身が書き出す `~/.claude/usage-data/session-meta/*.json` は重複排除しておらず、
行ごとの `usage` をそのまま合算した値になっています（対応するトランスクリプトが残っていた 27 セッション
全件で、重複排除しない生合計と誤差ゼロで一致することを確認済みです）。

そのため、**insights が算出する金額は Claude Code 側の表示より小さくなります。** これは insights 側の
バグではなく、重複排除の有無による差です。

## 何を評価するのか

`insights judge` は各セッションを 5 つの評価軸で判定します（`internal/model/model.go` の `Eval` 構造体、
`internal/judge/prompts/session_eval.md` に対応）。

1. **目的達成度と成果物の質**（`outcome` / `artifact_value`）: 何をしたかったのか、実際に達成されたか、
   成果物は資産として残る価値があったか（使い捨ての調査で終わっていないか）。
2. **介入コストと手戻り**（`intervention_cost` / `rework`）: 中断回数、軌道修正の指示、同じことの言い直し、
   ツールエラーの堂々巡り。「任せられなかった量」を見る軸で、介入が多かったこと自体を短絡的に悪と
   決めつけず、何が介入を必要にしたのかを判断材料にします。
3. **モデル選択・委譲の妥当性**（`model_fit`）: そのモデル・思考レベルは過剰だったか、不足だったか。
4. **学び・意思決定への寄与**（`learning_value`）: コードという成果物が出なくても、理解が進んだり
   意思決定の材料が得られたセッション（調査・比較検討・設計相談など）を拾う軸です。
5. **オーナーシップ／理解度**（`ownership`）: 内容を理解した上で指示し検収していたか。

   **丸投げそのものは悪ではありません。** 定型作業やルーティンワークの委譲は合理的な選択であり、
   それ自体を減点対象にはしません。問題になるのは、**本来理解しておくべき場面で理解せずに進めてしまう
   こと**、あるいは**何を任せたかを理解しないまま結果を検収せずに受け取ること**です。判定はこの区別に
   基づいて行われます。

**サブエージェント（sidechain）は個別に評価しません。** 委譲された作業は、親セッションの評価の中で
「委譲・オーナーシップの妥当性」として扱われます（子セッションへ何件・いくら・どれだけの時間を委譲したか
という要約だけが親セッションの評価材料として渡り、子の会話やツール呼び出しの中身自体は評価者に見えません）。

**コストも独立したセッションとしては計上せず、委譲元の親セッションに合算します。** セッションカードの
`ChildSessions` / `ChildCostUSD` に子の件数・コストが集計され、`TotalCostUSD`（親自身のコスト＋子のコスト）
がレポート上の金額として使われます。振り返りのプロジェクト別セッション一覧では、委譲を含むセッションが
「（委譲 N 件を含む）」と分かるように表示されます。サブエージェントは未評価セッション数
（`meta.unevaluated_sessions`）にも数えません（評価対象外であることと評価漏れは別の問題として扱うため）。

これは、実データでは全セッションのおよそ7割がサブエージェント実行であり、これらを個別に評価しても
費用対効果が悪く、改善提案が具体的な行動に繋がりにくいという判断によるものです。

評価軸の列挙値（`achieved` / `black_box` / `over` など）は、振り返り本文では日本語ラベルに変換されて
表示されます（`internal/render/labels.go` の変換表）。サイドカー YAML など機械可読な部分は元の英語の
列挙値のままです。

## 生成物

- `~/.insights/reports/daily/YYYY-MM-DD.md` — **日報**。その日何を成し遂げたかの記録。分析や反省は
  含めず、状況を知らない読み手にも伝わる粒度・人に共有できる粒度で書かれます。
- `~/.insights/reports/retro/YYYY-MM-DD.md` — **振り返り**。構成は「結論が先、数字は根拠として後ろ」。
  まず「今日投じたコストに見合った価値が出せたか」の結論（`worth_it` / `mixed` / `not_worth_it` /
  `insufficient_data` のいずれかの判定と、その理由の一言）を最初に示し、続けて**プロジェクト単位**で
  定量（セッション数・時間・コスト・コスト比率・達成率）と定性（AI が書く所見）の両面から振り返ります。
  評価軸の値は日本語ラベルで表示されます。価値への寄与が薄い小さなセッションは「その他 N 件」に畳まれ、
  **個別に載せるセッションが 1 件も無いプロジェクトは、プロジェクトごと「その他のプロジェクト」に
  畳まれます。** ただし成果が資産として残ったセッション（`outcome=achieved` かつ
  `artifact_value=durable`）は、小さくても個別に掲載されます。末尾に評価軸の全体分布（参考情報）・
  コストに見合わなかったセッション・新しい改善提案・前回までの提案の検証結果が続きます。
- `~/.insights/reports/meta/YYYY-MM-DD.yaml` — **サイドカー YAML**。日報・振り返りが参照する完全な
  構造化データ（モデル別・プロジェクト別集計、評価軸ごとの全分布、評価コスト、単価未登録モデルなど）。
- 任意期間の HTML レポート（既定 `~/.insights/reports/insights-<from>_<to>.html`。`insights report --out`
  で変更可）。単一ファイル・外部リソース依存なしで、日次のロールアップを級数として積み上げます。

日報・振り返りの Markdown 先頭の YAML フロントマターは**最小限**に絞られています
（`date` / `sessions` / `duration_minutes` / `cost_usd` / `achieved_ratio` / `prompt_version` /
サイドカー YAML への相対パス `meta` のみ）。フロントマターが巨大で本文が読みにくいというレビューを
受けての変更です。完全な構造化データはこのフロントマターではなく、上記のサイドカー YAML
（`<output.dir>/meta/YYYY-MM-DD.yaml`）に分離されており、**任意期間の再集計はこのサイドカー YAML から
復元できます**。`insights report` や他のツールは本文の Markdown をパースする必要はありません。

## 設定ファイル

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

## 設計と拡張点

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
