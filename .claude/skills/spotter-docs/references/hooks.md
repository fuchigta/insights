# フックの設置（`spotter hooks install`）

```
spotter hooks install [--hook <name>[,<name>...]] [--print] [--hooks-dir <dir>]
```

`commit-msg` フックから `spotter check --message "$1"` を、`pre-push` フックから
`spotter check --pre-push "$1"` を呼ぶように設置します。`--hook` を省略すると両方
設置します。

## 挙動

### `core.hooksPath` が未設定の場合

git が既定で使う hooks ディレクトリ（`git rev-parse --git-path hooks`。通常 `.git/hooks`）に
`.sample` 以外のフックファイルが 1 つでも既にある場合は、**`core.hooksPath` を設定せず**、
そのディレクトリへ直接設置します（あとは「既存のフックファイルがあるとき」と同じ判定）。
lefthook や pre-commit（Python 版）のように `core.hooksPath` を設定しないまま
`.git/hooks/` へ直接フックを書くツールと共存するためで、選んだフックに限らずディレクトリ
全体を見ます（`core.hooksPath` を設定すると、選ばなかったフックも含めてそれら全部が
無効になってしまうため）。

それ以外の場合（既定の hooks ディレクトリが空、または `.sample` しか無い場合）は、
`--hooks-dir`（既定 `.githooks`）にディレクトリを作り、`core.hooksPath` をそこに設定した
上で、選んだフックを新規に作成します。

### `core.hooksPath` が既に設定されている場合

**それを尊重します。** 新しいディレクトリに決め打ちで差し替えたりはしません。あとは
「既存のフックファイルがあるとき」と同じ判定に従います。

### 既存のフックファイルがあるとき

フックファイルが無ければ新規作成します。既にある場合は中身で判定が分かれます。

- spotter の管理ブロックを含む → 何もしません（`already`。次項「べき等性」）
- 管理ブロックを含まない → **書き込みません。** 呼び出し行（`--print` と同じもの）と
  案内を出力するだけにとどめ（`foreign`）、実行は非 0 で終了します

管理ブロックの無い既存フックに追記しない理由は、追記した呼び出しが実行されないまま
「設置済み」に見えてしまう組み合わせが複数あるためです（lefthook・husky v9・pre-commit の
具体例は [hook-runners.md](hook-runners.md)）。触れなかったフックについても他のフックの
作成は続けます。案内に従って、呼び出し行を使っているフックランナーの設定に組み込むか、
既存のフックファイルに手で追記してください。

### べき等性

一度設置した後にもう一度 `spotter hooks install` を実行しても、既に spotter の管理ブロックが
入っているフックには何もしません（`already` として報告するだけ）。管理ブロックは次のマーカーで
識別されます。

```sh
# --- spotter (managed) begin ---
...
# --- spotter (managed) end ---
```

このマーカーさえ残っていれば、内側の中身を手で書き換えても「設置済み」として扱われ、
`spotter hooks install` の再実行で上書きされることはありません（このリポジトリ自身が
`go run ./cmd/spotter check ...` に書き換えて使っている実例です。後述）。

判定・新規作成はフックごとに独立しています。例えば commit-msg だけ設置済みの
リポジトリで `spotter hooks install` を再実行すると、commit-msg は `already`、pre-push は
`created`（または spotter 以外が作った既存の pre-push フックがあれば `foreign`）として
報告されます。

## `--hook`: 設置するフックを絞る

```bash
spotter hooks install --hook commit-msg
spotter hooks install --hook commit-msg,pre-push
```

カンマ区切りで `commit-msg` / `pre-push` を指定します。省略時は両方です。commit-msg だけ
欲しい（pre-push はフックランナー側で別途組み込む、または push 前の検査は要らない）利用者は
`--hook commit-msg` を使います。未知の名前を指定するとエラーになり、選べる名前を示します。

## `--print`: 呼び出し行だけを出力する

```bash
spotter hooks install --print
# commit-msg: spotter check --message "$1"
# pre-push: spotter check --pre-push "$1"
```

何も変更せず、呼び出し行を出力するだけです。lefthook などの既存フックランナーの設定に、この
行をそのまま貼り付けたい場合に使います（具体的なレシピは [hook-runners.md](hook-runners.md)）。
`--hook` でフックを 1 つに絞ったときは、どのフック向けかはもう自明なので呼び出し行だけを
1 行出力します。

```bash
spotter hooks install --print --hook pre-push
# spotter check --pre-push "$1"
```

## pre-push フックの設置

pre-push フックは、push しようとしている範囲を CI と同じ基準で手元で先に検査するためのもの
です（何を検査するか・範囲をどう決めるかは「pre-push が検査する範囲」を参照してください）。
ここでは設置そのものの挙動だけを扱います。

pre-push フックは標準入力に push 対象の ref の並びを受け取ります。`spotter hooks install` が
生成する管理ブロックは、フック自身の標準入力をそのまま `spotter check --pre-push "$1"` に
引き継ぎます。

```sh
if command -v spotter >/dev/null 2>&1; then
  spotter check --pre-push "$1" || exit 1
else
  echo "spotter: コマンドが見つからないため検査をスキップします" >&2
fi
```

commit-msg と同じく、spotter が手元に無い場合は警告して素通りします（すり抜けは CI が最後の
歯止めになります）。

### 既存の pre-push フックへ手で追記するときの相性

案内に従って呼び出し行を既存の pre-push フックに手で追記する場合、標準入力の扱いに
注意してください。標準入力は 1 度読むと空になるため、**既存の pre-push フックの中身が
既に標準入力を全部読み切っている場合、後ろに足した spotter の呼び出しには何も渡りません。**
既存フックが `while read local_ref local_sha remote_ref remote_sha; do ...; done` の
ようにループで最後まで読み切る形になっていないか、追記後に確認してください。

### 他のフックランナーとの共存

フックファイルが spotter 以外に管理されている場合の具体的なレシピ（lefthook・husky v9・
pre-commit（Python 版））は [hook-runners.md](hook-runners.md) にまとめています。

## 設置状況の確認（`spotter doctor`）

```
spotter doctor
```

設定済みの検査一覧（type・granularity）に加えて、`core.hooksPath` の値と、commit-msg・
pre-push それぞれのフックファイルの状態を表示します。

- 無し
- あり（spotter を呼び出しています）
- あり（spotter は未設定。既存のフックに spotter 以外の内容があるときの表示です）
- lefthook・husky・pre-commit のフックファイルと判定できたときは、そのランナーの設定
  （`lefthook.yml` など・`.husky/<フック名>`・`.pre-commit-config.yaml`）に spotter の
  呼び出しがあるかも確かめ、「lefthook 経由で設定済み（lefthook.yml: pre-push.commands.spotter）」
  のように場所を表示します。見つからなければその旨を、lefthook の pre-push で
  `use_stdin: true` が無ければその付け忘れを表示します。これは助言で、不合格にはしません
  （設定は [hook-runners.md](hook-runners.md) 参照）

pre-push フックが無くても不合格にはしません（commit-msg と同じ扱いです）。
`required_version` を設定していれば、手元のバイナリがそれを満たすかもここで分かります
（[versioning.md](versioning.md) 参照）。

## commit-msg フックが見る範囲

`spotter check --message` はステージ済みの変更（HEAD とインデックスの比較）を見ますが、
squashed 粒度の検査（`doc-sync` など）は比較元を**未 push 範囲の起点**にします。未 push
範囲は、下記の pre-push が push しようとしている範囲を決めるのと同じ定義
（`HEAD --not --remotes`、マージコミットを除く）です。これにより `git commit --amend` で
作り直すコミットも、元コミットからの累積差分として検査されます（per-commit 粒度は
引き続き HEAD を比較元にします）。詳しくは
[granularity.md](granularity.md#staged-モードの比較元)を参照してください。

## マージコミットは検査しない

`git merge --no-ff` や `git pull` でマージが発生すると、コンフリクトが無くても
`commit-msg` フックは走ります。この時点で `MERGE_HEAD` が存在するコミットは、
（コンフリクト解消のためにその場で書き足した内容も含めて）検査せず成功終了します。
これは CI 側（[ci-integration.md](ci-integration.md)、`spotter range`）がマージコミット
自体を比較範囲から除外しているのと同じ扱いで、取り込む側のブランチに積まれていた各
コミットはマージされる前に手元・CI どちらでもそれぞれ検査済みだからです
（詳しくは [granularity.md](granularity.md) 参照）。

**限界**: コンフリクト解消のためにマージコミット上で書き足した内容自体は、手元でも
CI でも検査されません。マージがシークレットの混入や大きな不正な変更を持ち込む経路には
ならないという前提に立っています。

## push する前に CI と同じ range 検査を走らせる（`spotter check --pre-push`）

```
spotter check --pre-push <remote>
```

pre-push フックから `spotter check --pre-push "$1"` として呼びます。`$1` は git が
pre-push フックに渡す remote 名（URL のこともあります）で、範囲の計算には使わず、
検査に失敗したときの案内にだけ使います。`--message` / `--range` とは排他です。

push しようとしている内容は引数からは渡らないため、pre-push フックの標準入力
（`<local ref> <local sha> <remote ref> <remote sha>` の行が ref ごとに 1 行）を
`cmd.InOrStdin()` から読んで範囲を決めます。upstream の推測はせず、標準入力に現れた
ref だけを見ます。

### pre-push が検査する範囲（`internal/prepush`）

ref ごとに次の規則で範囲式を決めます（`internal/prepush` の `PlanRef`）。

- 範囲式は `<local> --not --remotes` を基本とし、remote sha が全 0 でなくローカルに
  コミットとして実在するとき（＝まだどのリモート追跡ブランチにも取り込まれていない
  コミット）だけ、追加の除外として remote sha を付け足します
  （`<local> --not --remotes <remote sha>`）。`--remotes` は既知の全リモート追跡ブランチを
  除外の起点にする git 標準のショートカットで、pre-push の時点ではこの push 個別の
  remote sha がまだ追跡ブランチに反映されていないことがあるため、確実に除外できるよう
  別立てで付け足します
- 削除 push（local sha が全 0）は検査しません
- local sha が commit に peel できない場合（tree だけを指す tag など）は検査しません
- peel した local が現在の HEAD と異なる場合は検査しません。設定・worktree 粒度の検査や
  command 型のスクリプトは作業ツリー由来で、HEAD 以外の状態を検査しても再現できない
  ためです。tag の push はここに該当することがあります
- 複数 ref を同時に push した場合、ref ごとに個別の範囲で検査します（squashed 粒度の
  検査も ref をまたいでまとめません）。同じ範囲式になる ref（同じコミットを複数の ref に
  push する場合など）は範囲検査を重複して走らせません。worktree 粒度の検査は
  呼び出し 1 回につき 1 回だけ実行します
- 検査する ref が 1 つも無い場合（削除 push や tag の push だけの場合など）は、worktree
  粒度も含めて何も検査しません

検査しない ref があっても不合格にはせず、その理由を必ず stderr に 1 行出します
（[docs/principles.md](principles.md) 約束 3。警告という中間の段階を作らないための
割り切りです）。設定の読み込みと `required_version` の確認は、`--pre-push` の呼び出し
1 回につき 1 回だけ行います。

標準入力に ref が 1 行も無い場合（git は通常 push 対象の ref ごとに 1 行渡すため、0 行の
まま `--pre-push` に届くのは想定外の状態です）も同様に不合格にはせず、理由を stderr に
出します。典型的には、フックランナーが pre-push フックの標準入力をコマンドに引き継いで
いないケース（lefthook で `use_stdin: true` を付け忘れた場合など）で起きます。何も検査
されないまま黙って通ってしまうと気づきにくいため、フックランナー側の設定を確認するよう
促す文言を添えます。

[`config-guard`](checks/config-guard.md) も、通常の検査の後に ref ごとの範囲式で
起動します（squashed 粒度なので、ここでも ref をまたいでまとめません）。比較元・終点の
和で走るかどうかを決めるため、`checks` に config-guard が無くても push しようとしている
範囲の比較元・終点のどちらかにあれば起動します。

### 失敗したとき

各検査が違反を表示した後、続けて次を出します。

- CI（`spotter range` + `--range`）でも同じ結果になること
- 検査した ref とその範囲式
- その範囲で `--config` のファイル（既定 `.spotter.yml`）を変更したコミットの一覧
  （検査を足したコミットがどれかを特定しやすくするため）
- 対処の案内（[docs/ci-integration.md](ci-integration.md) の「対処」を参照）

### `--no-verify` で飛ばせる

commit-msg フックと同様、`git push --no-verify` で pre-push フックをスキップできます。
最後に検査するのは CI（`spotter range` + `--range`）なので、約束1「手元で通ったものは
CI でも通る」には影響しません。push する前に気づけるようにするための位置づけです。

## 実例: インストール済みバイナリではなく `go run` を使う

`spotter` 自身の開発リポジトリのように、リポジトリの中身そのものが `cmd/spotter` を
持っている場合は、`go install` で取得した（≒ 別バージョンかもしれない）バイナリではなく、
**手元のソースを毎回 `go run` で検証**した方が「今のブランチの spotter」と「フックが
呼ぶ spotter」が常に一致します。`spotter hooks install` が生成した管理ブロックの中身を、
そのまま書き換えれば実現できます。

```sh
#!/bin/sh
# --- spotter (managed) begin ---
if command -v go >/dev/null 2>&1; then
  go run ./cmd/spotter check --message "$1" || exit 1
else
  echo "spotter: go コマンドが見つからないため検査をスキップします" >&2
fi
# --- spotter (managed) end ---
```

git の `commit-msg` フックは**リポジトリのトップレベルで実行される**ことが保証されて
いるため、`go run ./cmd/spotter` のような相対パス指定がそのまま機能します。

`pre-push` フックも同じ書き換えができます。標準入力は素通しするだけで構いません。

```sh
#!/bin/sh
# --- spotter (managed) begin ---
if command -v go >/dev/null 2>&1; then
  go run ./cmd/spotter check --pre-push "$1" || exit 1
else
  echo "spotter: go コマンドが見つからないため検査をスキップします" >&2
fi
# --- spotter (managed) end ---
```

`pre-push` フックも git がリポジトリのトップレベルで実行することを保証しているため、
同様に相対パス指定がそのまま機能します。
