# companion-files

触ったファイルに対して、必ず伴うはずの**相方ファイルが存在するか**を検査します。
本体が削除・改名されたのに相方だけが残っている**孤児**も検出します。

[`doc-sync`](doc-sync.md) は「A を**変更**したなら B も**変更**されていること」を見ますが、
これは「A を触ったなら B が**存在**すること」であって別の関心事です。まだ B が 1 度も
無い（doc-sync の言う「片方だけ変更」にすらならない）状態は doc-sync では捕まりません。
実装だけ作ってテストを作らない、コンポーネントだけ作って型定義を作らない、マイグレーションの
up だけ作って down を作らない、といったミスがこの検査の対象です。

起動粒度は [squashed](../granularity.md) 固定です。後から相方ファイルを足す・孤児を消す
コミットを積めば通ります（doc-sync と同じ意味論）。

## 設定

```yaml
checks:
  companion-files:
    type: companion-files
    companions:
      - paths: 'src/**/*.ts'
        companion: '{dir}/{name}.test.ts'
        reason: 'テストが無い'
        exclude: ['**/*.d.ts', 'src/types/**']

      - paths: 'db/migrations/**/*.up.sql'
        companion: '{dir}/{stem}.down.sql'
        reason: 'ロールバック用のマイグレーションが無い'
```

`commit-intent` も同じ「ルールの一覧」という形の設定を持ちますが、フィールドの形が
まったく違う（commit-intent は commit type ごとの条件、こちらはファイルパターンごとの
相方指定）ため、YAML キーはあえて別にしています（`rules` ではなく `companions`）。

**既定ルールはありません。** テストファイルの命名規約は言語ごとに全く違うため、Go 側に
ハードコードされた既定値は持ちません。下部の「レシピ」を参考にしてください。
`companions` が 0 件だと起動時エラーになります。

### `companions[].paths` / `companions[].companion` / `companions[].reason`（全て必須）

- `paths`: 対象にするファイルの [doublestar](https://github.com/bmatcuk/doublestar) パターン
- `companion`: 相方ファイルの候補パスを組み立てるテンプレート（下記の変数が使えます）。
  文字列 1 つでも、複数候補を並べた配列でもかまいません。配列の場合、**いずれか 1 つが
  存在すれば**満たされます（例: 単体テストか `testdata` のどちらかがあればよい、という運用）。
  候補が全て見つからない場合、違反表示には全候補を並べます
- `reason`: 違反表示に出す理由

テンプレートの固定部分（`{...}` 以外にそのまま書いた部分）に `..` セグメントや絶対パス
（先頭が `/`）を書くと起動時エラーになります。知らない `{変数}` も同様に起動時エラーです。

### `companions[].exclude`（省略可）

このルールから外す doublestar パターンの一覧。

### テンプレート変数

| 変数 | 値（`src/api/client.ts` の場合） | 値（`db/migrations/001.up.sql` の場合） |
|---|---|---|
| `{dir}` | `src/api` | `db/migrations` |
| `{name}` | `client`（**最後の**拡張子を除いたベース名） | `001.up` |
| `{stem}` | `client`（**最初の**区切りより前） | `001` |
| `{ext}` | `.ts` | `.sql` |
| `{path}` | `src/api/client.ts` | `db/migrations/001.up.sql` |

この 5 変数だけのシンプルな置換です。正規表現キャプチャによる汎用的な変換は設定が読みにくく
なるため採用していません。これで足りない場合は [`command`](command.md) 型検査を使ってください。

`{dir}` はリポジトリ直下のファイル（ディレクトリ部分が無い）では空文字列になります。
`src/api/client.ts` → `src/api`、`client.ts` → `` （空）。テンプレート展開後に
先頭の `/` が残らないよう自動で正規化されるので、`'{dir}/{name}.test.ts'` は
`client.ts` に対して `/client.test.ts` ではなく `client.test.ts` になります。

**`{name}`/`{ext}` は最後の `.` だけを区切りに使います。** `001.up.sql` のような複合拡張子
では `{ext}` は `.sql` のみ、`{name}` は `001.up` になります。

**`{stem}` は最初の `.` だけを区切りに使います。** ドット始まりでなければファイル名の最初の
`.` より前（`001.up.sql` → `001`、`client.ts` → `client`）。`.env` のようにドット始まりの
（隠し）ファイルでは、先頭のドットを除いた残りの中の最初の `.` まで（先頭のドットを含める）を
とります。残りにドットが無ければファイル名全体になります（`.env` → `.env`、
`.env.local` → `.env`）。複合拡張子を持つファイルの「意味のある先頭部分」だけを取り出したい
場合（up/down マイグレーションなど）は `{name}` ではなく `{stem}` を使ってください。

## 判定のしかた

`Source.ChangedFiles()` は追加と変更を区別しません（`--diff-filter=ACMR` をまとめて返す）。
そこで、この検査は次の 2 種類の判定を行います。

> 1. 変更集合に含まれる `paths` 一致ファイルについて、相方の候補が 1 つも比較の終点に
>    存在しないなら違反（**相方が無い**）
> 2. 削除・改名集合に含まれる `paths` 一致ファイルについて、相方の候補が比較の終点に
>    まだ存在するなら違反（**孤児**）

「比較の終点」は staged モードではインデックス、range モードでは `to` のツリーです
（`Source.Exists`）。手元（staged）で `git add` し忘れた相方も、range で過去の
コミットを検査する場合も、同じ基準で判定します。作業ツリーを直接見ることはありません。

追加・変更を区別する必要が無く、既存のインターフェースだけで実装できます。相方の**中身**が
更新されているかまでは見ません（それは doc-sync の守備範囲で、こちらは存在だけを見ます）。

### 孤児の検出

本体ファイルが削除・改名されると、相方だけが取り残されることがあります
（`git mv foo.go bar.go` で `foo_test.go` を追従させ忘れる、ファイルを消したのに
テストだけ残る、など）。この検査は削除・改名されたファイル（`Source.DeletedFiles()`。
改名は旧パスの削除として扱う）についても `paths`/`exclude` を通し、相方の候補を組み立てて
比較の終点にまだ存在するかを見ます。

ただし、**本体と相方が 1:1 に対応するテンプレート**（`{name}` / `{stem}` / `{path}` の
いずれかを含むもの）だけが対象です。`{dir}` だけで組み立てる共有型テンプレート
（例: `{dir}/README.md` のように、同じディレクトリの複数の本体が同じ 1 つの相方を
指しうるもの）は、本体が 1 つ削除されても他の本体がまだその相方を必要としている
可能性があるため、孤児検出の対象から外れます（誤検知を避けるため）。この判定はルール
（`companion` に書いた候補それぞれ）単位で行うため、1 つのルールに 1:1 対応の候補と
共有型の候補が混ざっている場合、1:1 対応の候補だけが孤児検出に使われます。

「相方が無い」と「孤児」は別の Violation として報告されます（同じ理由のルールでも、
それぞれ独立に集計されます）。`git mv foo.go bar.go` だけして `foo_test.go` を
追従させなかった場合、「`bar_test.go` が無い」と「`foo_test.go` が孤児」の両方が
出ます。

## 出力

```
companion-files の検査に失敗しました。

  テストが無い:
    - src/api/client.ts → src/api/client.test.ts

  テストが無い（本体が削除・改名されたのに相方が残っています）:
    - internal/foo/foo.go → internal/foo/foo_test.go
```

## 免除

[免除トレーラ](../exemptions.md)が使えます。既定のトレーラ名は `Companion-Files` です
（キー名を変えていれば、そのキーから生成された名前になります）。squashed 粒度なので、
範囲内のどれか 1 コミットのメッセージに書けば効きます。

## レシピ（設定例。既定値ではありません）

以下は「よくある命名規約の例」であり、spotter が既定で持つ値ではありません。必要なものだけを
自分のプロジェクトの `.spotter.yml` にコピーして使ってください。

### テスト

```yaml
# Go（同じディレクトリに _test.go）
- paths: 'internal/**/*.go'
  companion: '{dir}/{name}_test.go'
  reason: 'Go のテストが無い'
  exclude: ['**/*_test.go']

# TypeScript（同じディレクトリに .test.ts）
- paths: 'src/**/*.ts'
  companion: '{dir}/{name}.test.ts'
  reason: 'TypeScript のテストが無い'
  exclude: ['**/*.d.ts', '**/*.test.ts']

# Python（同じディレクトリに test_ プレフィックス。{name} だけでは組み立てられないため
# ディレクトリ構成を test_ 側に揃える運用が前提）
- paths: 'app/**/*.py'
  companion: '{dir}/test_{name}.py'
  reason: 'Python のテストが無い'
  exclude: ['**/test_*.py', '**/__init__.py']
```

### マイグレーション

```yaml
# up/down のペア。{stem} を使うことで "001.up.sql" のような複合拡張子でも
# "001.down.sql" を正しく組み立てられる（{name} だと "001.up.down.sql" になってしまう）
- paths: 'db/migrations/**/*.up.sql'
  companion: '{dir}/{stem}.down.sql'
  reason: 'ロールバック用のマイグレーションが無い'
```

### コンポーネントに付随するファイル

```yaml
# React コンポーネントにスタイルシートを伴わせる
- paths: 'src/components/**/*.tsx'
  companion: '{dir}/{name}.module.css'
  reason: 'コンポーネントのスタイルシートが無い'
  exclude: ['**/*.stories.tsx']
```

### 複数候補のいずれかでよい場合

```yaml
# 単体テストか testdata のどちらかがあればよい
- paths: 'internal/**/*.go'
  companion: ['{dir}/{name}_test.go', '{dir}/testdata/{name}']
  reason: 'テストか testdata のどちらも無い'
  exclude: ['**/*_test.go']
```
