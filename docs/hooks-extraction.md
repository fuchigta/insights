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

現在は TSV とテキストに散っている設定を、リポジトリ直下の 1 枚に寄せます。**「検査の実装
（type）を定義する場所」と「その type をインスタンス化してパラメータを与える場所」を分離する**
のが現在との一番の違いです（いまはスクリプトに焼き付いていて、読む側からは見えません）。

組み込みの type（`doc-sync` / `unwanted-files` / `doc-paths` / `commit-subject`）は暗黙に
登録済みで、`types` に書かなくても `checks` から使えます。`types` に同名で書いた場合は、
その組み込み type の既定値（`default`）を上書きする意味になります。外部コマンドを追加するときも
同じ `types` に登録し、`command` を持たせるだけです（`command` を特別扱いする専用の type 名は
作らず、「`command` フィールドを持つかどうか」で組み込みと外部コマンドを区別します）。

```yaml
types:
  commit-subject:
    default:
      exempt:
        enable: false     # メッセージの体裁そのものを検証する検査なので、免除は既定で不可にする

  my-custom-check:
    command: ./scripts/my-check.sh
    default:
      granularity: per-commit
      exempt:
        enable: true
    schema:                          # checks 側で渡せるオプションの形（省略可）
      simple:
        threshold: { type: integer, required: true }

  my-json-schema-check:
    command: ./scripts/other-check.sh
    schema:
      json-schema:                   # フル JSON Schema で書きたい場合はこちら
        type: object
        properties:
          pattern: { type: string, pattern: "^[a-z]+$" }
        required: [pattern]

checks:
  # 同じ doc-sync を用途別に複数インスタンス化する例。1 系統にまとめると、免除トレーラが
  # どの対応表にも効いてしまい「ザル」になるため、対応表ごとにキーを分ける。
  doc-sync-frontend:
    type: doc-sync
    pairs:
      - paths: "internal/cli/*.go"
        doc: README.md
        when: '^[+-].*(Use:|Short:|Flags\(\)\.|AddCommand\()'
    exclude: ["*_test.go"]          # テストは利用者に見える面を定義しないため

  doc-sync-backend:
    type: doc-sync
    pairs: [...]

  unwanted-files:
    type: unwanted-files
    max_bytes: 1048576
    deny:
      - { paths: "*.jsonl", reason: "取り込み元のセッションログ" }

  commit-subject:
    type: commit-subject            # types.commit-subject の default（exempt 不可）を継承する
    allowed_types: [feat, fix, perf, refactor, docs, test, build, ci, chore, revert]

  my-check-a:
    type: my-custom-check
    threshold: 10
  my-check-b:                       # 同じ type を設定違いで複数インスタンス化
    type: my-custom-check
    threshold: 20
    exempt:
      trailer: MyCheckB2             # enable は type の default(true) を継承、trailer だけ上書き
```

**固有性の高い検査を `command` で外部に逃がせる**ことは重要です。「汎用ツールに寄せられないから
全部自前のまま」にならずに済みます（`check-commit-types.sh` のような、抽出規則がファイル形式に
依存する検査の受け皿）。

#### `exempt` は常に object にする

`enable`（真偽値）と `trailer`（文字列）を持つ object 一択にします。boolean と object の
どちらも取れるユニオンにすると「object が来たら enable を true とみなすのか、type 側の
default から継承するのか」が曖昧になるためです。object 一択なら、次の優先順でフィールド単位に
マージするだけで済みます。

```
checks.<key>.exempt.<field>
  → types.<type>.default.exempt.<field>
    → システム既定（enable: true, trailer は <key> から自動生成）
```

`trailer` の既定値の生成元は **`type` ではなく `checks` のキー**にします。`type` から生成すると、
`doc-sync-frontend` / `doc-sync-backend` のように同じ type を複数インスタンス化したときに
トレーラ名が衝突してしまうためです。

#### `granularity` は組み込み type では固定にする

`exempt` とは違い、`granularity` は checks 側で上書きできるようにしません。`unwanted-files` の
`per-commit`（後から消しても履歴に残るから、という検査の性質そのものに由来する値）を
`squashed` に緩められてしまうと、§2 で挙げた「検査ごとに違う範囲の意味論を正しく表現する」という
差別化ポイント自体が checks 側の設定ミスで骨抜きになります。`command` 型は検査作者が
`types.<name>.default.granularity` で決め、そちらは上書き不可というルールが組み込み型と揃います
（checks 側からは変更できない値、という点で組み込み・command 型とも共通）。

#### 組み込み type とのキー衝突はエラーにする

`types` に組み込み type と同名（例: `doc-sync`）のエントリを書けるのは `default` を上書きする
ときだけです。そこに `command` まで書かれていたら（＝組み込み実装を外部コマンドで丸ごと
差し替えようとしているのか、単なる typo による衝突なのか区別が付かない）、設定エラーとして
起動時に拒否します。意図的な差し替えをしたい、という要望が実際に出てから、専用の書き方を
別途用意するかを検討します。

#### 組み込み type のオプション検証は `schema` を経由しない

`schema` は `command` 型のために導入した機構で、組み込み type（`doc-sync` の `pairs` など）は
Go の構造体タグによる検証のままにします。組み込みは検証した値をそのまま Go の型として
使う必要があるため、`schema` で検証してから改めて構造体にマッピングする層を挟むと、検証と
デコードが二重管理になり複雑化します。`command` 型は値を外部プロセスに渡すだけなので
`schema` による検証だけで完結しますが、この非対称は「組み込みと command 型を同列に扱う」という
方針とは別レイヤーの実装上の割り切りとして許容します（type の明示・複数インスタンス化・
`exempt`/`granularity` の共通ルールというレベルでは、両者は同列のままです）。

#### `schema` は 2 つの書き方をサポートする

`command` で外部検査を登録するときに渡せるオプションを検証したい一方、フル JSON Schema を
毎回書きたい人は多くないはずなので、省略記法とフル記法をどちらも受け付けます。

判別に専用の `kind` フィールドは置きません。`kind: simple` + `fields: {...}` のように分けると、
Go 側では「まず `kind` だけを読んでから、該当フィールドだけ具体型にデコードし直す」という
2 段階デコードが必要になります。代わりに `simple` / `json-schema` という**キー自体を
discriminator にする**（どちらか片方だけを持つ object）と、両方を `omitempty` で持つ構造体への
通常のデコードだけで済みます。

```go
type SchemaConfig struct {
    Simple     map[string]FieldSpec `yaml:"simple,omitempty"`
    JSONSchema *jsonschema.Schema   `yaml:"json-schema,omitempty"`
}
```

### `command` 型の入出力契約

外部コマンドに何をどう渡すかは、まだどこにも書いていませんでした。既存の `check-doc-sync.sh` /
`check-unwanted-files.sh` は変更ファイルリストも diff の中身も自分で `git` を呼んで取得しており、
この資産をそのまま活かせる形にします。**ホストが渡すのは「どの範囲を見るか」と「免除判定用の
メッセージ」だけ**にし、ファイルリストや diff は検査コマンド自身が `git` で取得します（`git` を
使う前提はこのツールの他の部分ですでに置いているので、検査コマンド側が依存しても違和感が
ありません）。

```
<command> --mode staged --message-file <path>
<command> --mode range  --from <sha> --to <sha> --message-file <path>
```

- 終了コード 0 = 成功、非 0 = 失敗。stderr に人間向けメッセージを出す、という今の規約のまま
- `--message-file` は改行やマルチバイト文字を安全に扱うため、環境変数ではなくファイル渡しにする
  （`commit-msg` フックが `msgfile` を渡す今のやり方と同じ）

**`granularity` に応じたプロセス起動の粒度制御はホストが担い、検査コマンドは「1 回の呼び出し
＝ 1 つの比較範囲」というモデルだけを知っていればよい**ようにします。

- `granularity: per-commit` → 範囲内のコミットごとに 1 回ずつ起動（`--from` はその親コミット、
  `--message-file` はそのコミット単体のメッセージ）
- `granularity: squashed` → 範囲全体で 1 回だけ起動（`--from` は範囲の始点の親、`--message-file`
  は範囲内の全コミットメッセージを連結したもの）

**免除判定（`exempt`）はホストが検査コマンドを起動する前に済ませます。** 免除に該当するなら
そもそも起動しないので、検査コマンド側はトレーラの正規表現を自分で持つ必要がありません
（`--message-file` は免除判定用ではなく、メッセージの中身自体を検証したい検査向けに残します。
`commit-subject` 相当を外部コマンドで作りたい場合の受け皿です）。

#### `schema` で検証したオプションの受け渡し（`transport`）

`schema` で定義した検査固有オプションをどう検査コマンドに渡すかは、`types` ごとに選べるように
します。

```yaml
types:
  my-custom-check:
    command: ./scripts/my-check.sh
    transport: args           # args | env | file（既定）
    schema:
      simple:
        threshold: { type: integer, required: true }
```

| transport | 渡し方 | 許容するスキーマ形状 |
|---|---|---|
| `file`（既定） | JSON 一時ファイル＋ `--options-file <path>` | 制限なし |
| `args` | `--<field> <value>` に展開。配列は繰り返し引数（`--paths a --paths b`） | トップレベル全フィールドがスカラー、またはスカラーの配列 |
| `env` | `GUARDS_OPT_<FIELD>=<value>` | トップレベル全フィールドがスカラーのみ |

`file` を既定にしたのは安全側に倒すためです。スキーマがスカラーのみでも、明示的に `args`/`env`
を選ばない限り JSON 渡しのままにしておけば、将来スキーマに配列フィールドを 1 つ足しても
既存の `command` 型検査が黙って壊れることがありません。

`args` はスカラー配列まで許容し、`env` はスカラーのみに制限するという非対称にしています。
`args` は配列を「同じフラグを繰り返す」という安全な表現で扱えますが、環境変数は 1 キーに
1 つの文字列しか持てないため、配列を表現するにはカンマ区切りや `_LENGTH` + 連番キーのような
細工が要ります。カンマ区切りは値自体にカンマを含むと壊れ、連番キー方式は POSIX shell に
間接参照（bash の `${!var}` 相当）が無いため `eval` を使った読み出しが必要になり、しかも
言語ごとに書き味の非対称が大きくなります。`args` に安全な代替がすでにある以上、`env` に
この複雑さを持ち込む理由は薄いと判断しました。

スキーマ形状が `transport` の制約に反する場合（例: `env` を指定したのにオブジェクト型
フィールドがある）は、`types` をロードする段階でエラーにします。

### CI 側の範囲算出

`.github/workflows/ci.yml` にある「PR なら `origin/<base>..HEAD`、push なら `before..sha`、
比較対象が無ければ `-1 HEAD`」の分岐は、どのプロジェクトでもそのままコピペされる部分です。
`guards range` として持たせると、CI 側の記述が `guards check --range "$(guards range)"` まで縮みます。
地味ですが、導入の手間に一番効きます。

**組み込みで自動検出するのは GitHub Actions と GitLab CI（セルフホスト含む）の 2 つに限定します。**
他の CI は将来的にも組み込みで持たず、`guards check --range <from>..<to>` に自分で組み立てた
範囲を渡してもらう形にします。CI ごとの検出ロジックは実機でしか検証しにくく、増やすほど
「Go を選んだのはテストを書けるから」という利点を削るためです。

両者とも本質的には同じ 3 パターン（① MR/PR イベント → base..head、② push イベント →
before..after、③ 判定できない・新規ブランチ等 → フォールバック `-1 HEAD`）に落ちるので、
共通ロジック 1 つ＋環境変数名の違いを吸収するアダプタという構成にします。

| | GitHub Actions | GitLab CI |
|---|---|---|
| 検出用フラグ | `GITHUB_ACTIONS=true` | `GITLAB_CI=true` |
| MR/PR イベント判定 | `GITHUB_EVENT_NAME=pull_request` | `CI_PIPELINE_SOURCE=merge_request_event` |
| その base | イベント JSON の `pull_request.base.sha` | `CI_MERGE_REQUEST_DIFF_BASE_SHA` |
| push イベントの before | `github.event.before` | `CI_COMMIT_BEFORE_SHA` |

自動検出は環境変数の有無で行い、`--provider github-actions|gitlab-ci` で明示上書きもできるように
します。どちらも、新規ブランチの最初の push や force push 直後は before 相当の SHA が
全部ゼロ（`0000...`）になることがあるため、そのフォールバックを両アダプタで揃える必要があります。
実装前に `.github/workflows/ci.yml` の該当分岐を読み直して、insights 側がすでにこのケースを
どう扱っているか確認してから仕様に落とします。

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
   （仮称 `consistency`）に一般化できるかを見てから決める。できなければ `command` を持つ
   type のまま残す

## 7. 決めていないこと

- リポジトリ名・コマンド名
- insights のリリースと同様にバイナリを配るか、`go install` だけにするか
- `schema` の `json-schema` 側で実際にどこまで検証するか（型チェックだけか、`pattern` /
  `enum` のような制約まで含めるか）。使う Go 側の JSON Schema 実装の選定も未着手
- GitLab CI 向けの range アダプタは insights 自身の CI（GitHub Actions のみ）では実地検証できない。
  導入する会社環境での動作確認が前提になる
- `doc-sync` の `pairs` のような行指向で見たい対応表を YAML の中でどこまで読みやすく保てるか
  （TSV は diff が読みやすいという利点があったが、YAML への統一自体はここまでの検討で
  自然に前提になっている）

[← README に戻る](../README.md)
