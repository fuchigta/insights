[← README に戻る](../README.md)

# フック群を外部ツールへ切り出す設計メモ

このリポジトリの `.githooks/commit-msg` と `scripts/check-*.sh` は、insights の題材（セッションログ）に
依存している部分がごく一部しかありません。**他のプロジェクトでもそのまま欲しくなる検査**なので、
別リポジトリの再利用可能なツール（フックの設置 + フックから呼ばれる CLI）として切り出す案を
ここに残します。

これは**決定ではなく判断材料**です。実装はまだ始めていません。

## 1. 何が汎用で、何が insights 固有か

現在の 5 つの検査を「他プロジェクトに持っていったときに何を書き換える必要があるか」で棚卸しすると、
書き換えが必要なのはほぼ**データ（表・パターン・閾値）だけ**で、判定の仕組み自体は固有ではありません。

| 検査 | 仕組みの汎用性 | insights 固有なのは |
|---|---|---|
| `scripts/check-doc-sync.sh` | 高い。最も価値がある | 対応表 `scripts/doc-sync.tsv` の中身だけ |
| `scripts/check-unwanted-files.sh` | 高い | 拒否する拡張子と理由（`*.jsonl` / `*.db` / `.insights/`）、サイズ閾値 |
| `scripts/check-doc-paths.sh` | 高い | 走査対象のドキュメントと、パスと見なすプレフィックス（`internal` / `cmd` / `scripts` ほか）、除外リスト `scripts/doc-paths-ignore.txt` |
| `scripts/check-commit-subject.sh` | 高いが**既存ツールが多い**（commitlint / cog など） | 許可する type 一覧と日本語のエラーメッセージ |
| `scripts/check-commit-types.sh` | 中。一般化はできるが抽出規則がファイル形式に依存する | 突き合わせる 3 箇所（`cliff.toml` / `scripts/check-commit-subject.sh` / `CLAUDE.md`）と、それぞれの抽出正規表現 |

つまり切り出す価値はあります。ただし**切り出す対象を間違えないこと**が重要です（次節）。

## 2. フックランナーは作らない

「フックの設置」は lefthook / husky / pre-commit がすでに解いている問題で、ここを自作すると
本体の価値と関係のないところ（設定ファイル形式・並列実行・ステージ済みファイルの受け渡し・
Windows 対応）を延々と面倒見ることになります。一方で、既存ツールに**無い**のは次の 3 つです。

1. **コミットメッセージのトレーラによる免除。** `Doc-Sync: skip <理由>` はメッセージが確定する
   `commit-msg` でしか読めません。環境変数の逃げ道にしなかった理由（手元で通した判断が CI に届かない）は
   そのまま引き継ぎます。既存ランナーは「メッセージを読んで検査自体を免除する」仕組みを持ちません。
2. **同じ検査を手元と CI が同じ引数で呼べること。** フックは clone に含まれず有効化も手動なので、
   すり抜けは必ず起こります。CI が最後の歯止めになる前提で、**検査を単体で実行できる CLI** である
   必要があります。
3. **検査ごとに違う「範囲の意味論」。** `doc sync` は範囲の端から端をひとまとめに見る（後から
   ドキュメントを直すコミットを足せば通る）のに対し、混入の検査はコミット単位で見る（後から消しても
   直らない）。この違いは意図的で、既存ランナーには表現する場所がありません。

**結論**: 作るのは「検査 CLI」。フックの設置は薄い補助にとどめ、lefthook などから呼ぶ形でも
成立するようにします（`core.hooksPath` は 1 つしか持てないため、他のフック管理と共存できないと
採用の障壁になります）。

## 3. CLI の形（案）

リポジトリ名・コマンド名は未決です。ここでは仮に `guards` と書きます。

```
guards install [--print]    # core.hooksPath を設定する / 既存フックへ追記する
                            # --print は呼び出し行だけを出す（他のランナーに貼る用）
guards check  --message <ファイル>   # ステージ済みの変更（commit-msg フック）
guards check  --range <git の範囲>   # 範囲（CI）
guards check  doc-sync --range ...   # 検査を 1 つだけ
guards range  --event pull_request   # CI 用の範囲算出（後述）
guards doctor                        # 有効な検査の一覧、フックが有効化されているか
```

`check` は**1 つ失敗しても残りを走らせ、終了コードだけを集約**します（直すたびに次の失敗が出てくると
フックを疎まれる、という現在の設計をそのまま持ち込みます）。

### 設定ファイル

現在は TSV とテキストに散っている設定を、リポジトリ直下の 1 枚に寄せます。検査ごとに
**範囲の意味論と免除トレーラを設定として明示する**のが現在との一番の違いです（いまはスクリプトに
焼き付いていて、読む側からは見えません）。

```yaml
checks:
  doc-sync:
    granularity: squashed        # 範囲の端から端をひとまとめに見る
    exempt_trailer: Doc-Sync     # 本文に `Doc-Sync: skip <理由>` があれば免除
    pairs:
      - paths: "internal/cli/*.go"
        doc: README.md
        when: '^[+-].*(Use:|Short:|Flags\(\)\.|AddCommand\()'
    exclude: ["*_test.go"]       # テストは利用者に見える面を定義しないため

  unwanted-files:
    granularity: per-commit      # 後から消しても直らないのでコミット単位
    exempt_trailer: Unwanted-Files
    max_bytes: 1048576
    deny:
      - { paths: "*.jsonl", reason: "取り込み元のセッションログ" }

  doc-paths:
    docs: ["README.md", "CLAUDE.md", "docs/*.md", ".github/*.md"]
    prefixes: ["internal", "cmd", "scripts", ".githooks", ".github"]
    ignore: [...]

  commit-subject:
    types: [feat, fix, perf, refactor, docs, test, build, ci, chore, revert]

  # 一般化が難しい検査は外部コマンドとして逃がす。
  commit-types:
    type: command
    run: ./scripts/check-commit-types.sh
```

最後の `type: command` は重要です。**固有性の高い検査の逃がし先**があることで、「汎用ツールに
寄せられないから全部自前のまま」にならずに済みます。

### CI 側の範囲算出

`.github/workflows/ci.yml` にある「PR なら `origin/<base>..HEAD`、push なら `before..sha`、
比較対象が無ければ `-1 HEAD`」の分岐は、どのプロジェクトでもそのままコピペされる部分です。
`guards range` として持たせると、CI 側の記述が `guards check --range "$(guards range)"` まで縮みます。
地味ですが、導入の手間に一番効きます。

## 4. 実装で詰まるところ

見落とすと「移植したら判定が変わっていた」という形で出るものを、先に書いておきます。

- **glob の意味論。** 現在は `case "$f" in $pat)` で判定しており、シェルの `*` は `/` にも一致します
  （`internal/source/*/*.go` のような書き方が成立しているのはこのため）。Go の `path.Match` は
  `*` が `/` に一致しないので、そのまま移すと対応表の行が**静かに発火しなくなります**。自前 glob か
  doublestar 相当が必要です。
- **言語は Go を推す。** 単一バイナリで配れること、Windows で git bash を前提にしないこと、
  検査そのものにテストを書けること（現在のシェルスクリプトは実質テストが無い）が理由です。
  依存する外部コマンドは `git` だけに保ちます。
- **免除トレーラに理由を必須にするか。** 現在の正規表現は `Doc-Sync: skip` だけでも通ります。
  理由なしの免除が溜まると検査の意味が薄れるので、新ツールでは理由の空文字を拒否する案があります
  （挙動が変わるので、移行時に既存履歴との整合を確認する必要あり）。

## 5. バイナリが無い手元でどうするか

フックから外部バイナリを呼ぶ以上、「入れていない人の手元」が必ず発生します。

| 案 | 評価 |
|---|---|
| 無ければ警告して素通り | **推奨。** フックが壊れず、すり抜けは CI が止める（現在と同じ哲学）|
| `go run` へフォールバック | 全員に Go を要求することになる |
| フックが自前でダウンロードする | コミットのたびにネットワークへ出る。供給網の観点でも採らない |

加えて、設定に `required_version` を持たせ、**古いバイナリでは失敗させる**ことを考えています。
検査が増えたのに手元のバイナリが古いと、「手元で通ったものは CI でも通る」という前提が崩れるためです。

## 6. 移行の段取り（insights 側）

一度に置き換えると、判定が変わったことに気付けません。**影運用で出力を突き合わせてから**
切り替えます。

1. 新リポジトリで `doc-sync` と `unwanted-files` を実装する（トレーラと範囲の意味論を含む）
2. insights の CI で**新旧の両方**を走らせ、同じ範囲に対する結果が一致することを確認する
3. 一致したら `.githooks/commit-msg` と `.github/workflows/ci.yml` を置き換え、
   `scripts/check-doc-sync.sh` / `scripts/check-unwanted-files.sh` と
   `scripts/doc-sync.tsv` を削除する。`CLAUDE.md` と [docs/development.md](development.md) の
   記述も同時に直す（対応表の書式が変わるため）
4. `commit-subject` と `doc-paths` を同じ手順で移す
5. `check-commit-types.sh` は最後。「ファイル + 抽出正規表現の集合を突き合わせる」汎用検査
   （仮称 `consistency`）に一般化できるかを見てから決める。できなければ `type: command` のまま残す

## 7. 決めていないこと

- リポジトリ名・コマンド名
- 設定は YAML 1 枚か、現在の TSV の読みやすさを残すか（対応表は行指向のほうが diff が読みやすい）
- insights のリリースと同様にバイナリを配るか、`go install` だけにするか
- 検査の追加を他人から受け付けるか（受け付けるなら、検査は設定で有効・無効を切り替えられる必要がある）

[← README に戻る](../README.md)
