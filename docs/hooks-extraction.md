[← README に戻る](../README.md)

# フック群を外部ツールへ切り出す設計メモ

このリポジトリにあった `.githooks/commit-msg` と `scripts/check-*.sh` は、insights の題材
（セッションログ）に依存している部分がごく一部しかありませんでした。**他のプロジェクトでも
そのまま欲しくなる検査**だったため、別リポジトリの再利用可能なツール（フックの設置 + フックから
呼ばれる CLI）として切り出しました。

**切り出しは完了しています。** 実装は [github.com/fuchigta/spotter](https://github.com/fuchigta/spotter)
にあります。insights 側は `spotter` を外部ツールとして利用するだけで、コードはもう
このリポジトリにはありません（`.githooks/commit-msg`・`.github/workflows/ci.yml` の
`repo guards`/`commit message` ジョブ・`.spotter.yml` を参照）。以下は判断材料として
検討した経緯・設計の記録です（過去形で書き直してはいませんが、実装済みという前提で読んでください）。

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

リポジトリ名・コマンド名は `spotter` に決定しました。「コーディングエージェントが自律的に作業する（＝コミットする）のを、
致命的な失敗の直前で支える」という役割を、ジムのスポッター（補助者）の比喩で表しています。「guardrail」ほど一般名詞化して
おらず、LLM 出力検証で知られる既存 OSS（Guardrails AI）との衝突も避けられます。

```
spotter install [--print]    # core.hooksPath を設定する / 既存フックへ追記する
                            # --print は呼び出し行だけを出す（他のランナーに貼る用）
spotter check  --message <ファイル>   # ステージ済みの変更（commit-msg フック）
spotter check  --range <git の範囲>   # 範囲（CI）
spotter check  doc-sync --range ...   # 検査を 1 つだけ
spotter range  --event pull_request   # CI 用の範囲算出（後述）
spotter doctor                        # 有効な検査の一覧、フックが有効化されているか
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

`doc-paths` と `consistency` は `squashed` / `per-commit` のどちらでもなく、3 つ目の値
`worktree` を持ちます。両方とも git の差分ではなく**現在の作業ツリーそのもの**を見る検査で、
staged/range の指定に関わらず 1 回だけ実行します。コミットメッセージに依存しないため、
免除トレーラの仕組み自体を持ちません（`doc-paths` の元のシェルスクリプトにもスキップの
逃げ道が無いことと対応します）。

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

`json-schema` 側の検証は型チェックだけでなく `pattern` / `enum` のような制約まで含めます。
`simple` 記法との差が「ネストできるかどうか」だけになってしまうと、フル記法を用意した意味が
薄れるためです。実装には **`santhosh-tekuri/jsonschema`（v6）を使います**。依存ゼロの pure Go
実装で、Draft 2020-12 まで対応しており、`kubeconform` など他ツールでの採用実績もあります。
`xeipuuv/gojsonschema` は定番ですが Draft-07 までしか対応せず更新頻度も落ちているため、
`schema` に将来新しい制約を足す余地を残す観点で見送りました。

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
| `env` | `SPOTTER_OPT_<FIELD>=<value>` | トップレベル全フィールドがスカラーのみ |

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
`spotter range` として持たせると、CI 側の記述が `spotter check --range "$(spotter range)"` まで縮みます。
地味ですが、導入の手間に一番効きます。

**組み込みで自動検出するのは GitHub Actions と GitLab CI（セルフホスト含む）の 2 つに限定します。**
他の CI は将来的にも組み込みで持たず、`spotter check --range <from>..<to>` に自分で組み立てた
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

**GitLab CI アダプタは実機検証を待たずに、公式ドキュメント（[Predefined variables](https://docs.gitlab.com/ci/variables/predefined_variables/)）
に基づいて実装します。** insights 自身の CI は GitHub Actions のみで GitLab CI 環境を持たないため、
実地検証できる状態を待っていると着手できません。GitHub Actions アダプタで固めた「共通ロジック＋
環境変数アダプタ」という構成なら GitLab 側だけ後から直しても影響範囲が閉じるので、まず公式仕様
どおりに実装し、実際に GitLab CI を使う環境に導入されたタイミングで動作確認・修正する、という
順序で進めます。

## 4. 実装で詰まるところ

見落とすと「移植したら判定が変わっていた」という形で出るものを、先に書いておきます。

- **glob の意味論。** 現在は `case "$f" in $pat)` で判定しており、シェルの `*` は `/` にも一致します
  （`internal/source/*/*.go` のような書き方が成立しているのはこのため）。Go の `path.Match` は
  `*` が `/` に一致しないので、そのまま移すと対応表の行が**静かに発火しなくなります**。自前 glob か
  doublestar 相当が必要です。
- **言語は Go を推す。** 単一バイナリで配れること、Windows で git bash を前提にしないこと、
  検査そのものにテストを書けること（現在のシェルスクリプトは実質テストが無い）が理由です。
  依存する外部コマンドは `git` だけに保ちます。
- **免除トレーラの理由は必須にする。** 現在の正規表現は `Doc-Sync: skip` だけでも通ってしまうが、
  CLAUDE.md 自体が「理由を添えて書く」運用を前提にしている以上、理由の空文字は新ツールでは拒否する。
  現行のシェルスクリプトとは挙動が変わる（＝理由なしの `skip` は通らなくなる）ため、移行時（§6）に
  過去の免除コミットとの整合は問題にならない（`spotter` は今後のコミットにのみ効くため）が、
  影運用の段階で新ツールが拒否したメッセージが無いか確認する。

## 5. バイナリが無い手元でどうするか

**配布はバイナリも作る**（`go install` だけにはしない）ことに決めました。insights と同じリリース
パイプライン（`cliff.toml` ベース）を流用でき、追加の運用コストがほぼゼロだからです。バイナリを
配ることで、手元に Go が無い開発者でも導入できます（Go が無いと `go install` すら選べず、
下の「無ければ警告して素通り」というフォールバックに全員が落ちてしまう）。

フックから外部バイナリを呼ぶ以上、それでも「入れていない人の手元」は必ず発生します。

| 案 | 評価 |
|---|---|
| 無ければ警告して素通り | **推奨。** フックが壊れず、すり抜けは CI が止める（現在と同じ哲学）|
| `go run` へフォールバック | 全員に Go を要求することになる |
| フックが自前でダウンロードする | コミットのたびにネットワークへ出る。供給網の観点でも採らない |

加えて、設定に `required_version` を持たせ、**古いバイナリでは失敗させる**ようにしました。
検査が増えたのに手元のバイナリが古いと、「手元で通ったものは CI でも通る」という前提が崩れるためです
（spotter リポジトリの `internal/version`。バージョン文字列を解釈できない場合、つまり
`go install`/`go run` でビルドしたことを示す既定値 `dev` のときは判定不能として満たしている
とみなし、ブロックしません）。

バイナリのビルド・配布は、切り出し後は spotter リポジトリ自身の `.github/workflows/release.yml`・
`cliff.toml`（`v*` タグ）で行います。insights はビルドせず、CI・commit-msg フックの両方が
GitHub Release からバイナリを取得して使うだけです（詳細は [docs/development.md](development.md) の
「CI」節、バージョン固定の方法を含む）。

## 6. 移行の段取り（insights 側）

**新規リポジトリから作り始めるのではなく、insights の中で作り切ってから切り出します。** 別々の
リポジトリで並行開発すると行き来のコストがかかるうえ、insights のフックに実際に差し込んで
使いながら直せるという利点を捨てることになるためです。切り出し自体は最後の 1 回だけ、
コミット履歴を持たずに行います（過渡期のルールは [CLAUDE.md](../CLAUDE.md) 参照）。

一度に置き換えると、判定が変わったことに気付けません。**影運用で出力を突き合わせてから**
切り替えます。

1. insights リポジトリ内に独立した Go module `spotter/` を作り、そこで `doc-sync` と
   `unwanted-files` を実装する（トレーラと範囲の意味論を含む）。`internal/` には依存しない
2. insights の CI で**新旧の両方**を走らせ、同じ範囲に対する結果が一致することを確認する
3. 一致したら `.githooks/commit-msg` と `.github/workflows/ci.yml` を `spotter` 呼び出しに
   置き換え、`scripts/check-doc-sync.sh` / `scripts/check-unwanted-files.sh` と
   `scripts/doc-sync.tsv` を削除する。`CLAUDE.md` と [docs/development.md](development.md) の
   記述も同時に直す（対応表の書式が変わるため）

   → 完了。5 つの検査すべてが揃った段階で、影運用（CI での比較実行）と Windows/macOS
   での動作確認（`spotter test` マトリクス）を済ませてからまとめて切り替えた。
   `.githooks/commit-msg` は `spotter check --config .spotter.yml` を呼び、
   `.github/workflows/ci.yml` の `repo guards` / `commit message` ジョブも同じ呼び出しに
   置き換わっている。旧シェルスクリプト 5 本と `scripts/doc-sync.tsv` /
   `scripts/doc-paths-ignore.txt` は削除済み。
4. `commit-subject` と `doc-paths` を同じ手順で移す（→ 上記と同時に完了）
5. `check-commit-types.sh` は最後。「ファイル + 抽出正規表現の集合を突き合わせる」汎用検査
   （仮称 `consistency`）に一般化できるかを見てから決める。できなければ `command` を持つ
   type のまま残す

   → 一般化できたため `consistency` として実装済み。`sources` に
   `{file, line, extract, split}` の列を書き、ファイルごとに「対象行を絞る正規表現
   （省略可）」「値を取り出す正規表現（キャプチャグループ 1 つ必須）」「取り出した値を
   さらに分割する区切り文字（省略可、`check-commit-subject.sh` の `PATTERN` から
   `feat|fix|...` を割るのに使う）」を指定する。全 `sources` の組み合わせで集合を
   突き合わせ、食い違いがあれば差分を報告する。`doc-paths` と同じ理由（git の差分ではなく
   現在の作業ツリーそのものを見る）で `granularity` は `worktree` 固定。
6. 5 つの検査すべてが `spotter/` 側に揃い、insights のフック・CI が完全に `spotter` 呼び出しに
   置き換わったら切り出す。新規リポジトリを作り、`spotter/` の中身をそのままコピーして
   module path を最終的なものに付け替えるだけでよい（コミット履歴は持っていかない）

   → 完了。[github.com/fuchigta/spotter](https://github.com/fuchigta/spotter) へコミット履歴
   無しでコピーし、`v0.1.0` を最初のリリースとして公開した（module path は
   `github.com/fuchigta/spotter` のまま付け替え不要）。CI/リリースワークフローも
   insights のものを流用し、タグは `spotter-v*` → `v*` に、`cliff.toml` の
   `tag_pattern`・コミットリンク先も新リポジトリ用に戻した。LICENSE（MIT）を新規に追加した
   （insights 自体にも合わせて追加した）
7. 切り出し後、insights 側の `spotter/` ディレクトリと `CLAUDE.md` の過渡期ルール節を削除し、
   `spotter` を外部ツールとして `spotter install` で導入し直す

   → 完了。`spotter/` ディレクトリ・`.github/workflows/spotter-release.yml`・CI の
   `spotter test`/`spotter format & tidy` ジョブを削除した。`.githooks/commit-msg` と
   `.github/workflows/ci.yml` の `repo guards`/`commit message` ジョブは、ローカルの
   `spotter/` をビルドする方式から、新リポジトリの GitHub Release からバイナリを取得する
   方式に変更した（`.sha256` でチェックサム検証、`.github/workflows/ci.yml` の
   `SPOTTER_VERSION` と `.spotter.yml` の `required_version` でバージョンを固定。
   自動追従にしていないのは、spotter 側のリリースで insights の CI が意図せず
   影響を受けないようにするため）。`CLAUDE.md` の過渡期ルール節は削除し、代わりに
   外部ツールとしての参照先だけを残した

## 7. 決めていないこと

- `doc-sync` の `pairs` のような行指向で見たい対応表を YAML の中でどこまで読みやすく保てるか
  （TSV は diff が読みやすいという利点があったが、YAML への統一自体はここまでの検討で
  自然に前提になっている）。運用してみて問題が出なかったため決着した（旧 issue #16）

[← README に戻る](../README.md)
