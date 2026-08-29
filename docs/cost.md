[← README に戻る](../README.md)

# コストの話

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
  `estimatedCostPerSessionSonnetUSD`）。この基準は「`--json-schema` 付きの最小呼び出しの実測(約
  $0.074。構造化出力がツール経由で実装されているため固定費が乗ります)を下回らないよう安全側に寄せた値」
  であり、スキーマ無しの実セッション評価の実測（約 $0.025）よりも高めに出ます。セッションの長さ・委譲件数
  によって実費は大きく変動するため、あくまで事前確認用の目安です。既定モデルは `claude-sonnet-5` で、
  `judge.model`（設定ファイル）で変更できます。
- **評価コスト自体もレポートに計上されます。** 日次ロールアップの `meta.judge_cost_usd` /
  `meta.judge_session_ids` として記録され、振り返りレポートの生成コストの中に含まれます。これは
  「振り返りのためにかけたコストが、振り返り自体で使い切れていないか」を自己監視するための仕組みです。
  評価の実行自体が作る Claude Code セッション（`~/.insights/judge-workspace` 配下）は、集計対象を
  汚染しないよう `ingest` の時点で自動的に除外されます。
  コストは評価結果と同じ行（`session_evals`）に保存されるため、`insights judge` で先に評価を済ませて
  から日報を作っても（`insights run` の経路を含む）同じ値が出ます。
- **レート制限を検知したら、その実行の残りの評価は打ち切ります。** `claude` がレート制限らしきエラーを
  返したときは、そのセッションだけの問題ではなくアカウント全体に効いている状態なので、未着手のセッションは
  評価せずに中止し、日報・振り返りの生成にも進みません（`insights run` は judge 段階で止まります）。
  制限中に叩き続けても失敗と待ち時間が増えるだけだからです。成功済みの評価は DB にキャッシュされるため、
  時間をおいて再実行しても評価し直しにはなりません。
- **単価未登録のモデルがあると、そのモデル分のコストは過小評価されます。** `prices.json` に無いモデル名
  （`non_billable` に列挙された疑似モデル名を除く）を使ったセッションは 0 円として扱われるのではなく、
  「単価不明」として別掲され、日報・振り返りの但し書きと `insights ingest` の警告に必ず出ます。

## キャッシュ課金について

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
生成のプロンプトに渡って `cost_observation` の所見に反映されます。サイドカー YAML（`docs/reports.md` 参照）
自体には日合計の比としては出ませんが、`by_model` の `cache_read_tokens` / `cache_write_tokens` から同じ比を
計算できます。

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

[← README に戻る](../README.md)
