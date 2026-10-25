# CI 連携（`spotter range`）

commit-msg フックはステージ済みの変更しか見られません。フックを回避してマージされた
コミット（`--no-verify`、フック未設置の環境、Web UI でのマージコミットなど）を最後に
拾うのが CI の役割です。CI では「どこからどこまでを見るか」という比較範囲そのものが
自明ではないため、`spotter range` がこれを自動検出します。

## 使い方

```yaml
- run: echo "RANGE=$(spotter range)" >> "$GITHUB_ENV"
- run: spotter check --config .spotter.yml --range "$RANGE"
```

`--provider` で自動検出をスキップして明示することもできます（`github-actions` |
`gitlab-ci`）。

## 自動検出のロジック

対応しているのは GitHub Actions と GitLab CI（セルフホスト含む）だけです。両者とも
本質的には次の 3 パターンに落ちます。

1. **MR/PR イベント** → `base..HEAD`
2. **push イベント** → `before..after`
3. **判定できない・新規ブランチ等** → フォールバック `-1 HEAD`（直近 1 コミットだけを見る）

### GitHub Actions

- 環境変数 `GITHUB_ACTIONS=true` で検出
- `GITHUB_EVENT_NAME` が `pull_request` / `pull_request_target` なら、`GITHUB_EVENT_PATH`
  の JSON から `pull_request.base.sha` を読み、`<base>..HEAD` にする
- それ以外（push 含む）は、同じ JSON の `before` フィールドと `GITHUB_SHA` から
  `<before>..<after>` を組み立てる

### GitLab CI

- 環境変数 `GITLAB_CI=true` で検出
- `CI_PIPELINE_SOURCE=merge_request_event` なら `CI_MERGE_REQUEST_DIFF_BASE_SHA` を使う
- それ以外は `CI_COMMIT_BEFORE_SHA`/`CI_COMMIT_SHA` を使う

### フォールバックが発動する条件

- base/before に相当する SHA が空
- その SHA がリポジトリ内に実在しない（`git cat-file -e` で確認）

後者は**新しいブランチの最初の push** や **force push 直後**で起こります。CI が渡す
「比較元」の SHA が全ゼロ（`0000...`）になったり、shallow clone でその SHA 自体を
取得できていなかったりするためです。この場合は直近 1 コミットだけを見る
`-1 HEAD` にフォールバックします（何も見ないよりは安全側に倒す設計です）。

## 対応外の CI

上記 2 つ以外の CI では自動検出しません（環境変数の組み合わせを増やすほど実機でしか
検証できない分岐が増えるため、意図的にここで線引きしています）。自分で組み立てた
`<from>..<to>` 形式の範囲式を `--range` に渡してください。

## 検査は実行時の設定で範囲内の全コミットを見る

`--range` は、範囲内のコミットをそれぞれのコミット時点の `.spotter.yml` ではなく、
実行時（HEAD・作業ツリー）の `.spotter.yml` で検査します。

そのため、範囲の途中に検査（`checks` へのキー追加など）を足すコミットが含まれていると、
それより前のコミット（コミットした時点のフックは通っていた）が CI でだけ新しい検査に
引っかかって落ちることがあります。

コミットごとにそのコミット時点の設定で検査しないのは、次の 2 つの理由からです。

- 途中のコミットで設定を緩めて違反を持ち込み、後のコミットで元に戻すと、最終的な
  差分には何も残らないまま検査や免除のルールを迂回できてしまいます。HEAD の設定で
  範囲内の全コミットを見ることが、これを防ぐ最後の砦になっています
- 古いコミット時点の設定を新しいバイナリで読めない場合（type やオプションの変更など）
  に実行エラーになり、履歴を書き換えないと直せなくなります

この振る舞いは [principles.md](principles.md) の約束1「手元で通ったものは CI でも通る」の
例外です。

### 対処

push する前に、CI と同じ範囲を手元で `spotter check --range <範囲>` にかけて確かめて
ください。`spotter check --pre-push`（pre-push フック）を設置していれば、push のたびに
これが自動で行われるため、push する前に気づけます（[docs/hooks.md](hooks.md) 参照）。
落ちた場合:

- まだ push していないなら、履歴を書き換えて各コミットを検査に通る内容へ直すか、
  免除トレーラを付けてください。per-commit 粒度の検査は各コミットを個別に見るので、
  後のコミットで違反を直しても通りません
- main などへ直接 push する運用に限っては、検査を足すコミットより前だけを先に push
  してから残りを push する方法も使えます（例:
  `git push origin <検査を足すコミットの親>:<ブランチ>`）
- **PR 運用ではこの「先に push」は効きません。** PR の CI は push のたびに
  base..head 全体を実行時の設定で見直すため、前回までの push 分も新しい設定で
  評価し直されます。検査を足すコミットより前を別の PR として先にマージしてから、
  残りを検査を足すコミットと一緒に PR にし直すなど、範囲を区切り直してください
  （既に push して CI が落ちた場合も同様です）

実行時の設定がそのまま検査に使われるので、`.spotter.yml` の変更は CODEOWNERS などの
レビューで守る価値があります。[`config-guard`](checks/config-guard.md) を導入すると、
`.spotter.yml` を緩めたまま残す変更を、比較元と終点の `.spotter.yml` の差分から
検出し、理由付きの免除トレーラなしには通さないようにできます。

## 実践例（このリポジトリの `.github/workflows/ci.yml`）

このリポジトリ自身も spotter を dogfooding しており、`go run` で常に手元のソースを検証
しています（インストール済みバイナリではなく、リポジトリ内の `cmd/spotter` を毎回ビルドして
使う。詳しくは [hooks.md](hooks.md) 参照）。

```yaml
spotter:
  name: spotter check
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v7
      with:
        fetch-depth: 0   # base/before の SHA をたどるためフル履歴が必要

    - uses: actions/setup-go@v7
      with:
        go-version-file: go.mod
        cache: true

    - name: spotter range
      id: range
      run: echo "range=$(go run ./cmd/spotter range)" >> "$GITHUB_OUTPUT"

    - name: spotter check
      run: go run ./cmd/spotter check --range "${{ steps.range.outputs.range }}"
```

`fetch-depth: 0` が無いと、shallow clone によって base/before の SHA が
「実在しない」と判定され、意図せず `-1 HEAD` フォールバックに落ちてしまう点に注意して
ください。
