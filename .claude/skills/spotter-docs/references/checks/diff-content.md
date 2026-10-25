# diff-content

差分の**追加行・削除行そのもの**を deny パターンと突き合わせる検査です。
[`unwanted-files`](unwanted-files.md) が「ファイルの deny」なのに対して、こちらは「行の deny」です。

コーディングエージェントの最頻の失敗は「直さずに黙らせる」ことです。型/lint エラーを抑制
コメントで消す、落ちるテストを skip/only で回避する、コンフリクトマーカーを残したままコミット
する、といったミスは行として差分に現れますが、linter はファイル全体を見るため既存コードの
違反で大量に落ちてしまい、結局オフにされがちです。この検査は**差分の追加行だけ**を見るので、
「新規混入だけを止める」が成立します。

起動粒度は [per-commit](../granularity.md) 固定です。範囲内のコミットごとに 1 回ずつ見るため、
後から該当行を消しても履歴には残った状態になり、検査は失敗し続けます。

## 設定

```yaml
checks:
  diff-content:
    type: diff-content
    deny:
      - pattern: '@ts-ignore|//\s*nolint|#\s*noqa'
        reason: '型/lint エラーの抑制'
      - pattern: '^<{7}|^={7}|^>{7}'
        reason: 'コンフリクトマーカー'
      - pattern: '\.(only|skip)\('
        reason: 'テストの絞り込み・スキップ残し'
        paths: '**/*.spec.js'
      - pattern: '^\s*(func Test|def test_)'
        reason: 'テストの削除'
        on: removed
```

**既定の deny はありません。** どの記法を違反とみなすかは利用者が書きます（Go/TypeScript/Python
などの言語固有の記法をこちら側にハードコードすることはありません）。`deny` が 0 件だと
起動時エラーになります（常に成功する無意味な検査を作らせないため）。

### `deny[].pattern` / `deny[].reason`（両方必須）

行に当てる正規表現（Go の [`regexp`](https://pkg.go.dev/regexp) 構文）と、違反表示に出す理由。
**1 行ずつ**当てるため、`^`/`$` はそのまま行頭・行末を意味します（`doc-sync` の `when` は差分
テキスト全体に当てる仕様のため `^[+-]` のように書く必要がありますが、こちらは不要です）。

正規表現を YAML に書くときのクォートは [config-reference.md](../config-reference.md#yaml-に正規表現を書くときの注意) を参照してください。

`deny` は上から順に評価し、同じ行に複数のルールが一致したら最初に一致したルールの `reason` を
使います。

### `deny[].on`（省略可、既定 `added`）

`added` なら追加行、`removed` なら削除行を見ます。追加行に対する既定の用途（抑制コメント・
デバッグ出力の追加など）と、削除行に対する用途（テストの削除など）を書き分けられます。

### `deny[].paths`（省略可、既定は全ファイル）

[doublestar](https://github.com/bmatcuk/doublestar) パターン。指定するとそのファイルだけを
対象にします。省略時は変更された全ファイルが対象です。

### `deny[].net`（省略可、既定 `false`。`on: removed` 専用）

`on: removed` のルールにだけ指定できます（`on: added`、または `on` を省略した既定の
`added` で `net: true` を指定すると起動時エラーになります）。

テスト関数の**改名**（`func TestOld(` を消して `func TestNew(` を足す）は、素朴な
「削除行を見て違反」では「テストの削除」と区別が付きません。`net: true` にすると、
**ファイルごとに**、pattern に一致する削除行の数が同じ pattern に一致する追加行の数より
多いときだけ違反にします。削除行数が追加行数以下なら、そのファイルは違反にしません。

```yaml
- pattern: '^\s*func Test\w+\('
  reason: 'テストの削除'
  on: removed
  paths: '**/*_test.go'
  net: true
```

- 改名（同じファイル内で削除 1・追加 1）は違反になりません。
- ファイルごと削除した場合は追加行が 0 なので、従来どおり違反になります。
- 違反になった場合、そのファイルで一致した削除行を**全部**報告します。削除行のうちどれが
  「本当に消えた」もので、どれが（同じ pattern に一致する別の行に）置き換わっただけかは
  区別できないためです。
- 判定はファイルごとです。**別のファイルへの移動**（A から削除して B に追加）は、A 側の
  追加行が 0 のままなので違反のままです。これは意図的な設計判断です。行単位でファイルを
  跨いだ対応関係を突き止めようとすると、たまたま同じ pattern に一致する無関係な行と
  誤って相殺してしまう恐れがあり、「削除を後から補えば通る」という per-commit の意味論を
  壊しかねないためです。

## 挙動の細部

- 対象は追加・変更・コピー・改名・削除されたファイルです。
- `on: added` は「**変更されたファイルの中の追加行**」を見ます（削除されたファイルには追加行は
  ありません）。
- `on: removed` は「**変更されたファイルの中の削除行**」と「**ファイルごと削除された場合の全行**」
  を見ます。これにより、テスト関数や重要なコードの削除を検知できます。
- ファイルのリネームでは、git が差分をどう出すかによって全行が追加/削除に見える場合があります。
- バイナリファイルの差分は `Binary files ... differ` の 1 行になり、`+`/`-` で始まらないため
  自然に対象から外れます。
- 行番号は `@@ -a,b +c,d @@` ハンクヘッダから相対的に算出します。`on: added` なら新ファイル側
  （`+c` 起点）、`on: removed` なら旧ファイル側（`-a` 起点）の番号です。ハンクヘッダの形式が
  崩れていて起点を読み取れない場合、そのハンクの行番号は 0 として表示します（直前のハンクの
  番号を引きずって誤った行番号を報告し続けないようにするため）。
- 120 文字を超える行は表示時に切り詰めます。文字数は rune 単位で数えるため、日本語などの
  マルチバイト文字が途中で壊れることはありません。

## 出力

```
diff-content の検査に失敗しました（a1b2c3d feat: ...）。

  型/lint エラーの抑制:
    - src/api/client.ts:42: // @ts-ignore
```

同じ理由の違反は 1 つにまとめて表示されます。

## 免除

[免除トレーラ](../exemptions.md)が使えます。既定のトレーラ名は `Diff-Content` です
（キー名を変えていれば、そのキーから生成された名前になります）。ただし per-commit 粒度
なので、免除トレーラは違反した**そのコミット自身**のメッセージに、理由を添えて書く必要が
あります（例: `Diff-Content: skip 外部 API の型定義が壊れているため一時的に抑制`）。

## レシピ（設定例。既定値ではありません）

以下は「よくある違反パターンの書き方の例」であり、spotter が既定で持つ値ではありません。
必要なものだけを自分のプロジェクトの `.spotter.yml` にコピーして使ってください。

### 型/lint エラーの抑制コメント

```yaml
- pattern: '@ts-ignore|@ts-expect-error'
  reason: 'TypeScript の型エラー抑制'
- pattern: '//\s*nolint'
  reason: 'Go の lint 抑制'
- pattern: '#\s*noqa'
  reason: 'Python の lint 抑制'
- pattern: '//\s*eslint-disable'
  reason: 'ESLint の抑制'
```

抑制を「理由付きなら許す」運用にしたい場合は linter 側の仕組み（golangci-lint の
nolintlint の `require-explanation`、typescript-eslint の `ban-ts-comment`、
`eslint-comments/require-description`、Ruff の `PGH003`）が向きます。diff-content は
「新しく足すこと自体を止め、例外は免除トレーラに理由を書く」運用向けです。

### テストの skip / only 残し

```yaml
- pattern: '\.(only|skip)\('
  reason: 'テストの絞り込み・スキップ残し'
  paths: '**/*.{test,spec}.{js,ts}'
- pattern: '^\s*(t\.Skip|t\.SkipNow)\('
  reason: 'Go テストのスキップ残し'
  paths: '**/*_test.go'
- pattern: '@pytest\.mark\.skip'
  reason: 'pytest のスキップ残し'
  paths: '**/test_*.py'
```

### デバッグ出力・コンフリクトマーカー

```yaml
- pattern: '^<{7}|^={7}|^>{7}'
  reason: 'コンフリクトマーカー'
- pattern: '\bconsole\.log\('
  reason: 'デバッグ出力の残留'
- pattern: '^\s*fmt\.Println\('
  reason: 'デバッグ出力の残留'
```

### テストの削除

`net: true` を付けると、テスト関数の改名（削除して同じ名前規則の関数を足すだけ）は
違反にせず、正味で減った場合だけ捕まえられます。

```yaml
- pattern: '^\s*func Test\w+\('
  reason: 'Go テストの削除'
  on: removed
  paths: '**/*_test.go'
  net: true
- pattern: '^\s*(it|test)\('
  reason: 'JS/TS テストの削除'
  on: removed
  paths: '**/*.{test,spec}.{js,ts}'
  net: true
```

### 省略のプレースホルダ

コーディングエージェントがファイルの一部だけを示すつもりで
`// ... existing code ...` や `# ... 既存のコードはそのまま ...` のようなプレースホルダの
コメントを、そのまま差分として混入させてしまうことがあります。

```yaml
- pattern: '(//|#)\s*\.\.\.?\s*(existing|rest of|unchanged|既存|省略)'
  reason: '省略のプレースホルダ'
- pattern: '(//|#)\s*(rest of (the )?(code|file)|existing code)\b'
  reason: '省略のプレースホルダ'
```

### 失敗の握りつぶし（ワークフロー・シェル）

```yaml
- pattern: '^\s*continue-on-error:\s*true'
  reason: 'ワークフローの失敗を握りつぶす'
  paths: '.github/workflows/*.{yml,yaml}'
- pattern: '\|\|\s*true\s*$'
  reason: 'シェルコマンドの失敗を握りつぶす'
  paths: '**/*.sh'
```

言語ごとの空 catch や未チェックのエラーは各言語の linter（errcheck、ESLint の
`no-empty`、Ruff の `BLE001` など）の方が正確なので任せ、ここでは linter が見ない
箇所だけを拾います。`|| true` は意図して使う場面もあるので、その場合は免除トレーラに
理由を書いてください。

### アサーションの正味の減少

```yaml
- pattern: '\bt\.(Error|Errorf|Fatal|Fatalf)\('
  reason: 'Go テストのアサーション減少'
  on: removed
  paths: '**/*_test.go'
  net: true
- pattern: '\bexpect\('
  reason: 'JS/TS テストのアサーション減少'
  on: removed
  paths: '**/*.{test,spec}.{js,ts}'
  net: true
- pattern: '^\s*assert\b'
  reason: 'Python テストのアサーション減少'
  on: removed
  paths: '**/test_*.py'
  net: true
```

テストを通すためにアサーションを消すことはこれで捕まえられますが、期待値だけを
書き換える変更（件数が変わらない）は捕まえられません。それはミューテーションテスト
（Go なら [gremlins](https://github.com/go-gremlins/gremlins)、JS/TS なら
[Stryker](https://stryker-mutator.io/) など）のほうが向いています。
