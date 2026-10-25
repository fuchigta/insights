# 検査の粒度（granularity）

`spotter check --range` で範囲を指定したとき、検査は「範囲全体をまとめて 1 回見る」のか
「コミットごとに 1 回ずつ見る」のかで意味が変わります。この違いを **granularity** と呼び、
検査ごとに固定です（`checks` 側からは上書きできません）。

staged モード（`--message` を使う commit-msg フック向け）でも、ステージ済みの変更を 1 回
見るだけである点は granularity に関わらず共通です。ただし比較元は squashed と
per-commit（worktree は比較元を持ちません）で異なります。per-commit は常に HEAD を
比較元にしますが、squashed（`doc-sync` など）は未 push 範囲の起点（後述）を比較元に
します。詳しくは「staged モードの比較元（squashed と per-commit の違い）」を参照して
ください。

range モードでどの検査をどう見るかは常に実行時の `.spotter.yml` で決まります
（[ci-integration.md](ci-integration.md)）。

どちらのモードも**マージコミットは対象になりません**。range モードは対象コミットの
一覧を作るときにマージコミットそのものを除外し、staged モードは `MERGE_HEAD` が
残っている（＝マージの途中である）ときに検査自体をスキップします
（[hooks.md](hooks.md)）。取り込まれる側の各コミットは、
マージされる前に手元・CI どちらでもそれぞれ検査済みという前提です。

## 3 種類

### `squashed`（例: `doc-sync`, `companion-files`, `config-guard`）

範囲全体を、最古のコミットの親から最新のコミットまでの 1 回の比較としてまとめて見ます。

これは「後からドキュメントを直すコミットを足せば通る」ことを意味します。1 コミット目で
コードだけ変更し、2 コミット目で対応するドキュメントを直せば、範囲全体で見たときには
両方変更されているので通ります。人間が実際に開発するときの「まずコード、後でまとめて
ドキュメント」という進め方を妨げません。

免除トレーラも、範囲内の**どれか 1 つ**のコミットメッセージにあれば効きます
（`RangeMessages` が範囲内の全コミットのメッセージをコミットごとに分けて返し、
免除判定はコミットごとにトレーラ段落を取り出して行うため。[exemptions.md](exemptions.md)
参照）。ただし [config-guard](checks/config-guard.md) は対象を絞らない全体免除を
受け付けないため、範囲内のどれか 1 つのコミットに全体免除があっても範囲全体は免除されません
（[exemptions.md](exemptions.md#対象を絞らない全体免除を受け付けない検査)参照）。

### `per-commit`（例: `unwanted-files`, `commit-subject`, `diff-content`, `commit-intent`, `diff-size`）

範囲内のコミットごとに、そのコミット単体の親からの差分を 1 回ずつ見ます。

これは「後から消しても履歴には残る」性質を持つ検査に使います。一度コミットしてしまった
シークレットファイルや不正な commit-subject は、次のコミットで削除・修正しても git の
履歴上は残り続けます。squashed のように「範囲全体で見て最終的に無事ならOK」にはできません。

免除トレーラは、違反した**そのコミット自身**のメッセージに書く必要があります。

### `worktree`（例: `doc-paths`, `consistency`, `doc-links`）

staged/range の指定に関わらず、**現在の作業ツリーの中身を 1 回だけ**見ます。git の差分にも
コミットメッセージにも一切依存しません。

そのため worktree 粒度の検査には**免除トレーラの仕組みがありません**（何を免除するかを
コミット単位で判断する余地が無いため）。誤検知を抑えたい場合は、各検査が個別に持つ
`ignore` 相当のオプションを使ってください。

## まとめ

| granularity | 単位 | 免除トレーラ | 該当する組み込み検査 |
|---|---|---|---|
| `squashed` | 範囲全体で 1 回 | 範囲内のどれか 1 コミット | `doc-sync`, `companion-files`, `config-guard` |
| `per-commit` | コミットごとに 1 回 | そのコミット自身 | `unwanted-files`, `commit-subject`, `diff-content`, `commit-intent`, `diff-size` |
| `worktree` | 作業ツリーを 1 回 | 無し | `doc-paths`, `consistency`, `doc-links` |

組み込み検査の granularity は固定ですが、[command 型](checks/command.md)（外部コマンド
検査）では `types.<name>.default.granularity` に `squashed` / `per-commit` / `worktree`
のいずれも指定できます。`worktree` を選んだ場合、外部コマンドには `--mode worktree` が
渡されます（[command 型の入出力契約](checks/command.md)参照）。

## staged モードの比較元

staged モードの比較元は per-commit と squashed で異なります。

- per-commit は常に HEAD を比較元にします
- squashed は**未 push 範囲の起点**を比較元にします。未 push 範囲は、pre-push フックが
  push しようとしている範囲を決めるのと同じ定義（`HEAD --not --remotes`、マージコミットを
  除く。[hooks.md](hooks.md) の「pre-push が検査する範囲」参照）で、HEAD 自身を含みます。
  未 push のコミットが無ければ比較元は HEAD、リモート追跡 ref が 1 つも無い場合やコミットが
  1 つも無いリポジトリでは履歴全体と比較する重さを避けるため常に HEAD です（push しない
  ので pre-push との食い違いも起きません）。免除トレーラの判定も、これからコミットする
  内容のメッセージだけでなく、未 push のコミット（HEAD を含む）のメッセージを合わせて
  見ます

これは `git commit --amend` 対策です。git は commit-msg フックに amend かどうかを渡さない
ため、比較元を常に HEAD にしていると、amend で作り直されるコミットの本当の差分
（HEAD^ とインデックスの比較）のうち HEAD からの増分しか見えず、squashed の doc-sync 等が
誤検知したりすり抜けたりします。比較元を未 push 範囲の起点にすれば、amend でも通常の
コミットでも「起点からインデックスまで」の差分は同じ（作られるコミットの tree は常に
インデックスのため）になり、amend かどうかを知る必要が無くなります。push 済みのコミットは
未 push 範囲に含まれないため、この振る舞いが push 済みの履歴にまで及ぶことはありません。

command 型の staged 起動には、squashed でも比較元は渡りません（`--from`/`--to` は range
モードだけ）。比較元をまたぐ検査は pre-push フックと CI の range モードで確定します
（[checks/command.md](checks/command.md) 参照）。

## 「終点」の参照

`Source.Exists()` は、比較の**終点**（staged ならインデックス、range なら `to` のツリー）に
そのパスのファイルが存在するかを返します。この「終点」は起動 1 回分の比較が指す終わりのことで、
squashed なら範囲全体の最新コミット、per-commit ならそのコミット単体を指します（worktree
粒度の検査には `Source` 自体が渡らないため関係ありません）。`Source.DeletedFiles()`
（削除されたファイルの一覧。改名元のパスも含む）とあわせて、検査が「削除されたか」
「終点で見て残っているか」を判定するために使えます。

## 範囲式は `<from>..<to>` に限らない

`--range` に渡す範囲式は、`git rev-list`/`git log` にそのまま渡せる任意の引数列を
許します。`internal/rangespec` の `Plan` は範囲式を `strings.Fields` で単語に分けて
gitutil の `RevListNoMerges` / `RangeMessages` に渡すだけで、`<from>..<to>` の形を
前提にした解析はしていません。`spotter check --pre-push` が ref ごとに組み立てる
`<local> --not --remotes [<remote sha>]` のような複数語の式も、この経路をそのまま
通ります（[hooks.md](hooks.md) 参照）。

## 比較の両端のファイルを読む（EndpointReader）

`Source` は差分・ファイル一覧しか見せませんが、検査によっては比較の**両端**それぞれの
ファイルの中身そのものを読みたいことがあります（設定ファイルを比較の前後で解析する
`config-guard` など）。この場合、検査は `ctx.Source` を任意インターフェイス
`check.EndpointReader` に型アサーションして使います。

```go
type EndpointReader interface {
	BaseFile(path string) (data []byte, ok bool, err error)
	TargetFile(path string) (data []byte, ok bool, err error)
}
```

`Base`/`Target` の指す先はモードによって変わります。

| メソッド | staged モード | range モード |
|---|---|---|
| `BaseFile` | 比較元（前節の通り per-commit は HEAD、squashed は未 push 範囲の起点） | `from` |
| `TargetFile` | インデックス（`Source.Exists` と同じ終点） | `to` |

- そのパスがその時点に存在しなければ `ok=false`（`err` は git 自体の実行に失敗した場合
  だけに使う。存在しないことと実行エラーを区別するのは `Source.Exists` と同じ理由）
- staged モードで比較元が HEAD かつコミットが 1 つも無いリポジトリでは HEAD 自体が
  無いため、`BaseFile` は常に `ok=false`
- 比較元が根コミットを含む範囲の起点（`EmptyTree`）のときも、空ツリーには何も無いため
  `BaseFile` は常に `ok=false`（staged の squashed で根コミットまで未 push の場合、
  range モードで `from` が `EmptyTree` の場合のどちらも該当）
- `Source` に含めていないのは、足すと全ての組み込み検査のテストが使う fake `Source` を
  書き換えることになる上、使うのは比較の両端を読む検査だけのため。実装していない
  `Source`（テストの fake など）に型アサーションすると `ok=false` で失敗する

`internal/gitutil` では `stagedSource` / `rangeSource` の両方がこのインターフェイスを
実装しています。

## `config-guard` は起動そのものが実行時の設定だけでは決まらない

「range モードでどの検査をどう見るかは常に実行時の `.spotter.yml` で決まる」という
冒頭の原則には、[`config-guard`](checks/config-guard.md) だけ例外があります。他の
squashed 粒度の検査は「実行時の設定に無ければ走らない」ですが、config-guard は
比較元・終点のどちらかの `.spotter.yml` にあれば実行時の設定に無くても走ります
（詳しくは [checks/config-guard.md](checks/config-guard.md) の「起動する条件」を
参照）。`.spotter.yml` 自身を比較する検査の性質上、比較元・終点それぞれの設定を
見ないと「その差分自体が緩和かどうか」を判定できないためです。

## 免除の対象を検査の一部に絞る（ScopedExemptable）

`Runner` は任意で `ScopedExemptable`（`ExemptTargets() []string` を持つ）を実装できます。
実装した検査は、免除トレーラを検査全体ではなく `Violation.Target` 単位に絞れます
（[exemptions.md](exemptions.md) のスコープ付き免除を参照）。実装していない検査にスコープ付き
免除のトレーラを書くと、cli 側が黙って無視せず error にします。

さらに任意で `ScopedOnly` を実装すると、対象を絞らない全体免除を一切受け付けなくできます
（`ExemptTargets` と組み合わせて実装する。[exemptions.md](exemptions.md#対象を絞らない全体免除を受け付けない検査)参照）。
config-guard がこれを実装しています。

## 抽出結果を説明する（Explainer）

worktree 粒度の検査は任意で `Explainer`（`Explain(ctx) (Explanation, error)` を持つ）を
実装できます。実装した検査は、判定に使う抽出結果（ブロックの行範囲、抽出した要素と出現位置）を
合否を付けずに返せ、`spotter config explain <検査名>` から確認できます
（[config-reference.md](config-reference.md) 参照）。`Run` と同じ抽出処理を通すので、表示と判定が
ずれません。0 件やブロックの終端なしのように `Run` が実行エラーにする状況は、`Explain` では
説明に含めて正常終了します。
