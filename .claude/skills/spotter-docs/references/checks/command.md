# command 型

組み込み検査に寄せられない、プロジェクト固有の検査は外部コマンドとして登録できます。
`spotter` はコマンドの起動・引数の受け渡し・終了コードの解釈だけを引き受け、検査ロジック
そのものはシェルスクリプトでも Go 製バイナリでも何でも構いません。

このページは仕様のリファレンスと、実際に 1 つ作ってみるチュートリアルの 2 部構成です。

---

## 1. 仕様リファレンス

### 登録（`types`）

```yaml
types:
  my-check:
    command: ./scripts/my-check.sh
    transport: file        # file（既定） | args | env
    schema:
      simple:
        threshold: { type: integer, required: true }
    default:
      granularity: squashed # squashed | per-commit | worktree

checks:
  my-check:
    type: my-check
    threshold: 10
```

| フィールド | 必須 | 説明 |
|---|---|---|
| `command` | ✔ | 実行するコマンド（PATH 上のコマンド名でも相対/絶対パスでも可） |
| `args` | - | `command` に続けて渡す固定引数。省略可 |
| `transport` | - | `checks` 側のオプションをコマンドにどう渡すか。既定 `file` |
| `schema` | - | `checks` 側で渡せるオプションの形。省略すると検証しない |
| `default.granularity` | ✔ | range モードでの起動粒度。`checks` 側からは上書き不可 |

実際に起動されるコマンドラインは `<command> <args...> --mode ... --message-file ...`
の順になります。

シェバン付きのスクリプトファイルをそのまま `command` に指定する方式（例:
`command: ./scripts/my-check.sh`）は、**Windows では動作しません**（`os/exec` は
シェバンを解釈しないため）。`bash` を明示的に `command` にして、スクリプトを `args`
で渡す形にすれば OS を問わず動きます（Windows でも Git 同梱の `bash` が使えます）。

```yaml
types:
  my-check:
    command: bash
    args: [scripts/my-check.sh]
    default:
      granularity: per-commit
```

検査ロジックを Go で書きたい場合は、`go run` を経由する形でも同様に組み立てられます。

```yaml
types:
  my-check:
    command: go
    args: [run, ./cmd/my-check]
    default:
      granularity: per-commit
```

`types.<name>` は「組み込み type と同名なら default の上書き」「それ以外の名前なら
`command` を伴う新規登録」のどちらかにしか使えません。両方の意味を同時には持てません
（組み込み type と同名で `command` を指定するとエラーになります）。

### 入出力契約

コマンドは `default.granularity` に応じて次のいずれかで呼び出されます。

```
<command> --mode staged   --message-file <path> [オプション...]
<command> --mode range    --from <sha> --to <sha> --message-file <path> [オプション...]
<command> --mode worktree --message-file <path> [オプション...]
```

- `--mode staged`: commit-msg フックから、ステージ済みの変更を見るとき。`granularity: squashed`
  でも比較元（未 push 範囲の起点）は渡りません（`--from`/`--to` は range モードだけ）。
  そのため `git commit --amend` や未 push のコミットをまたぐ差分は、staged 起動の時点では
  スクリプト自身では判定できず、pre-push フックと CI の range モードで確定します
  （[granularity.md](../granularity.md#staged-モードの比較元)参照）
- `--mode range`: CI から、`--from`/`--to` の比較を見るとき
- `--mode worktree`: `granularity: worktree` のとき、staged/range を問わず常にこのモードで
  呼ばれます。差分という概念が無いため `--from`/`--to` は渡りません。コマンド自身が
  カレントディレクトリ（リポジトリのルート）以下を直接読んで検査してください
  （組み込みの `doc-paths`/`consistency` と同じ考え方です）
- `--message-file`: そのコミット（またはこれからコミットされる内容）のメッセージ本文が
  書かれたファイルへのパス。免除トレーラの判定は `spotter` 本体が既に済ませているので、
  ここでは主にメッセージの中身自体を検証したい場合に使います。`--mode worktree` では
  worktree 粒度の検査が免除トレーラの仕組み自体を持たないため、中身は空になります
- ファイル一覧や diff は渡されません（`--mode worktree` を除く）。**コマンド自身が
  `git diff` 等で取得してください**（`--from`/`--to` があれば
  `git diff --name-only $from $to` のように）

### 終了コード

- `0`: 成功（違反なし）
- 非 `0`: 失敗。標準エラー出力の内容がそのまま違反として表示されます

### `transport`: オプションの渡し方

`checks.<key>` に書いた、組み込みフィールド以外のキー（`threshold` など）が「オプション」
として集約され、`transport` に応じて次のように渡されます。

#### `file`（既定）

```
--options-file <一時ファイルのパス>
```

オプション全体を JSON にエンコードした一時ファイルを渡します。配列やネストしたオブジェクト
も含め、YAML で書けるものはそのまま渡せます。

#### `args`

```
--threshold 10 --tag foo --tag bar
```

各オプションを `--<field> <value>` に展開します。配列は同じフラグを繰り返します。
**スカラー（文字列・数値・真偽値）と、スカラーの配列のみ**対応します。

#### `env`

```
SPOTTER_OPT_THRESHOLD=10
```

`SPOTTER_OPT_<FIELD>`（フィールド名を大文字化し `-` を `_` に置換）という環境変数で渡します。
**スカラーのみ**対応します（配列を安全に表現する方法が無いため）。

### `schema`: オプションの検証

`checks` 側のオプションを、コマンドを起動する前に検証できます。`simple` と `json-schema` は
どちらか一方だけを指定してください（両方指定すると設定エラーになります）。

#### `simple`

```yaml
schema:
  simple:
    threshold: { type: integer, required: true }
    tags: { type: array, items: string }
```

`type` は `string` / `integer` / `number` / `boolean` / `array` のいずれか。`array` は
`items` でスカラー要素の型を指定します。`simple` に無いキーが `checks` 側にあるとエラーに
なります（未知のオプションを弾く）。

#### `json-schema`

```yaml
schema:
  json-schema:
    type: object
    properties:
      threshold: { type: integer, minimum: 1 }
    required: [threshold]
```

フルの JSON Schema（Draft 2020-12）が使えます。`pattern` や `enum`、`minimum` のような
`simple` では表現できない制約が必要なときに使ってください。

---

## 2. チュートリアル: 最小の検査コマンドを作る

「変更されたファイルの中に TODO コメントが増えていないか」を見る、シェルスクリプトの
検査コマンドを作ってみます。

### 2.1 コマンドを書く

```sh
#!/bin/sh
# scripts/check-no-todo.sh
set -eu

mode=""
from=""
to=""
message_file=""

while [ $# -gt 0 ]; do
  case "$1" in
    --mode) mode="$2"; shift 2 ;;
    --from) from="$2"; shift 2 ;;
    --to) to="$2"; shift 2 ;;
    --message-file) message_file="$2"; shift 2 ;;
    --options-file) options_file="$2"; shift 2 ;;
    *) shift ;;
  esac
done

if [ "$mode" = "staged" ]; then
  files=$(git diff --cached --name-only --diff-filter=ACMR)
  diff_cmd="git diff --cached -U0 --"
else
  files=$(git diff --name-only --diff-filter=ACMR "$from" "$to")
  diff_cmd="git diff -U0 $from $to --"
fi

found=""
for f in $files; do
  case "$f" in *.go) ;; *) continue ;; esac
  if $diff_cmd "$f" | grep -qE '^\+.*TODO'; then
    found="$found $f"
  fi
done

if [ -n "$found" ]; then
  echo "新しく TODO が追加されています:$found" >&2
  exit 1
fi
exit 0
```

ポイントは、**`--mode`/`--from`/`--to` を受け取って、ファイル一覧や diff は自分で
git から取る**ことです（`spotter` は範囲の算出とオプションの受け渡しだけを担当します）。
未知のフラグ（`--options-file` など、このコマンドが使わないもの）は無視して構いません。

この例は `granularity: per-commit`（`--mode` は `staged`/`range` のいずれか）を前提に
`if`/`else` の 2 分岐にしています。`granularity: worktree` を使う場合は `--mode worktree`
も明示的に分岐してください（`$from`/`$to` が空のまま `else` 節に落ちるとエラーになります）。

### 2.2 登録する

`command` に `bash` を指定し、スクリプトのパスは `args` で渡します（`bash` がスクリプトを
引数として読んで実行するので、実行権限を付ける必要も無く、Windows でも Git 同梱の `bash`
でそのまま動きます）。

```yaml
types:
  no-todo:
    command: bash
    args: [scripts/check-no-todo.sh]
    default:
      granularity: per-commit

checks:
  no-todo:
    type: no-todo
```

オプションを使わないので `schema` は省略しています（渡すオプションが無ければ検証も不要）。

### 2.3 動かしてみる

```bash
spotter doctor
# - no-todo（type=no-todo, granularity=per-commit）と表示されれば登録成功

spotter check no-todo --range "HEAD~5..HEAD"
```

TODO を追加した変更を含むコミットがあれば、スクリプトの標準エラー出力がそのまま
違反として表示されます。

### 2.4 オプションを持たせてみる

「見つけた TODO の許容件数」をオプション化する場合は次のようにします。

```yaml
types:
  no-todo:
    command: bash
    args: [scripts/check-no-todo.sh]
    transport: args
    schema:
      simple:
        max_allowed: { type: integer, required: true }
    default:
      granularity: per-commit

checks:
  no-todo:
    type: no-todo
    max_allowed: 3
```

`transport: args` にしたので、スクリプト側は `--max_allowed 3` を追加でパースすれば
受け取れます。`schema.simple` で型と必須を宣言したので、`checks.no-todo.max_allowed` に
文字列を書いてしまった場合などは `spotter` 側で起動前にエラーになります。

---

これで組み込み検査と全く同じように、commit-msg フックと CI の両方から同じ検査コマンドが
呼ばれるようになります。免除トレーラの扱いも組み込み検査と同様に効きます
（[exemptions.md](../exemptions.md) 参照）。
