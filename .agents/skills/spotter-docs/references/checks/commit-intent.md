# commit-intent

コミットメッセージの **type/scope** と、実際に変更されたファイル・差分の**内容**が
噛み合っているかを検証する検査です。

[`commit-subject`](commit-subject.md) が subject の**体裁**（Conventional Commits の形として
正しいか）だけを見るのに対し、こちらは**実態**（申告した type と実際の差分が一致しているか）
を見ます。

- `docs:` と名乗りながらコードを書き換えている
- `fix:` なのにテストが 1 行も増えていない
- `refactor:`（振る舞いを変えない）と言いながら新しい定義を生やしている

こうした「メッセージは体裁として正しいのに実態と噛み合わない」ミスは、commit-subject
（体裁のみ）でも一般的な linter（差分を見ない）でも検知できません。spotter は commit-msg
フックとして**メッセージと差分の両方を同時に持っている**ため、ここを検証できます。

起動粒度は [per-commit](../granularity.md) 固定です。メッセージと差分が 1:1 で対応していないと
意味を持たないため、`squashed`（範囲内の全メッセージを連結する）は選べません。

## 設定

```yaml
checks:
  commit-intent:
    type: commit-intent
    rules:
      # docs を名乗るならドキュメントだけを触る
      - types: [docs]
        allow: ['**/*.md', 'docs/**']
        reason: 'docs はドキュメントだけを変更する'

      # 振る舞いを変えるならテストが一緒に来る
      - types: [feat, fix]
        require: ['**/*_test.*', '**/test_*', '**/*.test.*']
        reason: '振る舞いの変更にはテストを伴う'

      # refactor を名乗って新しい定義を生やしていないか
      - types: [refactor]
        deny_diff: '^\+\s*(func|def|class) '
        reason: 'refactor で新しい定義が増えている'
```

**既定ルールはありません。** 特に `require` に書くテストファイルの命名は言語ごとに違うため、
Go 側にハードコードされた既定値は持ちません。下部の「レシピ」を参考に、自分のプロジェクトの
言語・命名規則に合わせて書いてください。`rules` が 0 件だと起動時エラーになります。

### `rules[].types`（必須）

このルールを適用する commit type の一覧。コミットメッセージの type がここに含まれない
場合、このルールはそもそも評価されません。

### `rules[].scopes`（省略可）

指定すると、その scope のときだけこのルールを適用します（省略時は scope を問いません）。

### `rules[].breaking`（省略可）

指定すると、コミットが [Conventional Commits](https://www.conventionalcommits.org/) の
破壊的変更（subject の `!`、例: `feat(api)!: ...`。または本文フッタの `BREAKING CHANGE:` /
`BREAKING-CHANGE:`）かどうかでこのルールの適用を絞ります。

- `true`: 破壊的変更のときだけ適用する
- `false`: 破壊的変更でないときだけ適用する
- 省略: 問わない（既定）

破壊的変更には移行手順のドキュメントを伴わせる例:

```yaml
- types: [feat, fix]
  breaking: true
  require: ['docs/**', 'CHANGELOG.md']
  reason: '破壊的変更には移行手順のドキュメントを伴う'
```

### `rules[].allow` / `rules[].require` / `rules[].deny` / `rules[].deny_diff`（少なくとも 1 つ必須）

| キー | 判定 | 違反になる条件 |
|---|---|---|
| `allow` | 変更・削除ファイルが**全て**いずれかの doublestar パターンに一致する | 一致しないファイルが 1 つでもある |
| `require` | 変更ファイルの**少なくとも 1 つ**がいずれかの doublestar パターンに一致する | どれにも一致するファイルが無い |
| `deny` | 変更・削除ファイルの**いずれか 1 つでも**doublestar パターンに一致する | 一致するファイルが 1 つでもある |
| `deny_diff` | 変更・削除ファイルの差分（追加・削除行を含む全体）に正規表現が一致しない | 一致してしまう |

`allow` は「これだけしか触ってはいけない」（ホワイトリスト）、`deny` は「これだけは触っては
いけない」（ブラックリスト）です。両方を同時に指定することもできます。

`allow` と `deny` と `deny_diff` は**削除ファイルも見ます**（`docs:` と名乗りながらコードを
削除しても逃げられないように）。削除ファイルが違反に含まれる場合、パスの末尾に `（削除）` を
付けて表示で削除だとわかるようにします。一方 `require` は「変更ファイルの少なくとも 1 つ」を
求めるルールなので、**削除では満たしたことになりません**（テストを消したコミットで、消した
テストファイル自身が require を満たしてしまっては本末転倒なため）。

`deny_diff` は既定では差分テキスト全体（ヘッダ・追加行・削除行を含む）に当てます。`doc-sync`
の `when` と同じく `(?m)` を自動付与するため、`^`/`$` はそのまま行頭・行末に効きます（例の
`^\+\s*(func|def|class) ` は「行頭が `+`（追加行）で始まり、その後に `func`/`def`/`class` が
続く行」に一致します）。

### `rules[].on`（省略可、`deny_diff` 指定時のみ）

`deny_diff` の対象を追加行・削除行だけに絞ります。指定すると、ヘッダを除いた行の中身
（先頭の `+`/`-` は落とした状態）に 1 行ずつ正規表現を当て、一致した行を
`path:line: text`（[diff-content](diff-content.md) と同じ形式）で違反に出します。

正規表現を YAML に書くときのクォートは [config-reference.md](../config-reference.md#yaml-に正規表現を書くときの注意) を参照してください。

- `added`: 追加行だけを見る
- `removed`: 削除行だけを見る
- 省略: 差分テキスト全体に当てる（従来どおり）

`deny_diff` を指定せずに `on` だけ指定すると起動時エラーになります。単一の追加/削除行だけを
見るなら、より柔軟な [diff-content](diff-content.md) の利用も検討してください（`diff-content`
は commit type を問わず行単位の deny を持てますが、コミットの申告 type とは連動しません）。

1 つのルールに `allow`/`require`/`deny`/`deny_diff` を複数指定すると、それぞれ独立して評価され、
違反ごとに別の結果として表示されます。

### `rules[].reason`（省略可）

違反表示に出す説明。省略すると `allow`/`require`/`deny`/`deny_diff` の内容から既定文言を
組み立てます。`require` 違反だけは、`reason` を指定していても「次のいずれかに一致する
変更が必要: `パターン一覧`」を表示に必ず添えます（`reason` だけでは、テストの命名規則を
複数書いているようなケースでどのパターンが不足なのか分からないため）。

```
振る舞いの変更にはテストを伴う（次のいずれかに一致する変更が必要: **/*_test.go, **/test_*.py）:
```

## 判定の細部

- 1 つのコミットに複数のルールが一致する場合、**全て**評価します。
- コミットメッセージの subject が Conventional Commits の一般形になっていない場合、
  この検査は**何も報告しません**（体裁の検証は `commit-subject` の責務であり、両方で
  落とすと同じ問題を 2 回報告することになるため）。
- subject が空、または `git revert` が生成する `Revert "<元の subject>"`（ダブルクォート
  付き）の形の場合も対象外です。「`Merge ` で始まる」は判定しません（マージコミット自体は
  手元・CI どちらでも別の仕組みで既に検査対象から外れているため。
  [hooks.md](../hooks.md) 参照）。`Merge stuff` や `Revert stuff` のように
  Conventional Commits の一般形に合わない subject は、上の「一般形になっていない場合は
  何も報告しない」規則にそのまま従います。
- 変更ファイルと削除ファイルが両方とも 0 件のコミットはスキップします（削除だけの
  コミットは検査を続けます）。

## 免除は既定で有効

`commit-intent` は差分側の実態を見る検査なので、`commit-subject`（体裁の検証を体裁で
回避する矛盾を避けるため既定で免除無効）とは異なり、**既定で免除トレーラが有効**です。
「テスト不要な軽微な修正」のような正当な例外はあり得るため、免除トレーラに理由を書かせる
ことに価値があります。

```
Commit-Intent: skip 型定義のみの変更でテストの追加は不要なため
```

## レシピ（設定例。既定値ではありません）

以下は「よくある使い方の例」であり、spotter が既定で持つ値ではありません。必要なものだけを
自分のプロジェクトの `.spotter.yml` にコピーして使ってください。

### ドキュメントのみを名乗るなら、コードを触らせない

```yaml
- types: [docs]
  allow: ['**/*.md', 'docs/**']
  reason: 'docs はドキュメントだけを変更する'
```

### 振る舞いの変更にはテストを伴わせる

```yaml
# Go
- types: [feat, fix]
  require: ['**/*_test.go']
  reason: '振る舞いの変更には Go のテストを伴う'

# Python
- types: [feat, fix]
  require: ['**/test_*.py', '**/*_test.py']
  reason: '振る舞いの変更には pytest のテストを伴う'

# JS/TS
- types: [feat, fix]
  require: ['**/*.test.{js,ts}', '**/*.spec.{js,ts}']
  reason: '振る舞いの変更には JS/TS のテストを伴う'
```

### refactor で新しい公開 API を生やしていないか

```yaml
# Go（新しい関数定義の追加行を検知）
- types: [refactor]
  deny_diff: '^\+\s*func [A-Z]'
  reason: 'refactor で新しいエクスポート関数が増えている'

# Python
- types: [refactor]
  deny_diff: '^\+\s*(def|class) '
  reason: 'refactor で新しい定義が増えている'
```

### refactor で新しい公開 API を生やしていないか（`on` を使う場合）

`on: added` を使うと、`^\+` を書かずに追加行だけへ絞り込め、違反表示も
`path:line: text` になって一致箇所がすぐ分かります。

```yaml
- types: [refactor]
  deny_diff: '^func [A-Z]'
  on: added
  reason: 'refactor で新しいエクスポート関数が増えている'
```

### chore/build はソースコードを触らせない

```yaml
- types: [chore, build]
  allow: ['**/*.yml', '**/*.yaml', '**/*.json', 'Makefile', 'Dockerfile']
  reason: 'chore/build は設定ファイルだけを変更する'
```

### 生成ファイルを手で編集させない

```yaml
- types: [feat, fix, refactor, chore]
  deny: ['**/*_generated.go', '**/*.pb.go']
  reason: '生成ファイルは生成コマンドで作る（手編集しない）'
```

### 品質ゲートの設定を変えたら理由を求める

`commit-subject` の `allowed_types` を YAML アンカーにして参照すると、commit type の
一覧を二重管理せずに済みます。`types` に全 type を指定すれば、そのファイルを触る
コミットは type を問わず必ずこのルールにかかります。免除トレーラ名はキーから作られる
ため、キーを別に切ると `Commit-Intent` とは別のトレーラ名になります。

```yaml
checks:
  commit-subject:
    type: commit-subject
    allowed_types: &commit-types [feat, fix, perf, refactor, docs, test, build, ci, chore, revert]

  quality-gate-config:
    type: commit-intent
    rules:
      - types: *commit-types
        deny: ['.golangci.yml', '.eslintrc*', 'eslint.config.*', 'ruff.toml', '.testcoverage.yml', 'codecov.yml']
        reason: '品質ゲートの設定を変えるときは理由を書く'
```

キー `quality-gate-config` から作られるトレーラ名は `Quality-Gate-Config` です。

```
Quality-Gate-Config: skip 基準値を今のカバレッジまで引き上げる
```

この検査自体を `.spotter.yml` から消せば止められなくなります（range モードは
実行時の `.spotter.yml` で検査するため。[ci-integration.md](../ci-integration.md)
参照）。`.spotter.yml` の変更自体は CODEOWNERS などのレビューで守ってください。
