# `.spotter.yml` 全体リファレンス

既定のパスは `.spotter.yml`（`--config` で変更可）です。トップレベルは 3 つのキーだけを
持ちます。

```yaml
required_version: v0.1.0

types:
  <type 名>: { ... }

checks:
  <キー>: { ... }
```

## `checks`

実際に実行される検査のインスタンス一覧です。キー名は自由に付けられます（表示名・
デフォルトの免除トレーラ名・`spotter check <キー>` での個別実行に使われます）。

```yaml
checks:
  <キー>:
    type: <組み込み type 名 | types に登録した名前>
    exempt:
      enable: true    # 省略時 true（commit-subject のみ false）
      trailer: Custom  # 省略時はキーから自動生成（例: doc-sync → Doc-Sync）
    # 以下は type ごとのフィールド
```

`type` は必須です。組み込み type（`doc-sync` / `unwanted-files` / `doc-paths` /
`commit-subject` / `consistency` / `diff-content` / `commit-intent` / `companion-files` /
`doc-links` / `diff-size` / `config-guard`）ならそのまま使えます。それ以外の名前を
指定する場合は、`types.<type>` に `command` を登録しておく必要があります（無ければ
設定エラー）。`config-guard` だけは `checks` に置けるのが 1 つまでです（2 つ目を
置くと `Load` がエラーにします。比較元・終点どちらの `.spotter.yml` にも
`config-guard` のキーが高々 1 つになり、判定が一意に決まるようにするためです）。

`type` ごとのフィールドは各検査のページを参照してください。機械可読なカタログ（type・
起動粒度・免除の既定値・設定キー一覧）が必要な場合は `spotter checks --json` を使ってください
（この一覧を静的に保持しているのは `internal/cli/checks.go` で、`internal/cli/checks_test.go`
が組み込み type の一覧・起動粒度とのズレを検知します）。

`.spotter.yml` に知らないキー（typo を含む）を書くと `Load` が起動時エラーにします。
トップレベル（`required_version`/`types`/`checks`）、`types.<name>` とその
`default`、`checks.<key>.exempt`、`pairs`/`sources`/`rules`/`companions` の各要素などは、
ゼロ値を明示的に書いた場合も含めてこの対象です。組み込み type の `checks.<key>` 直下（例:
`commit-subject` に `doc-sync` 用の `pairs` を書いた場合）と、`unwanted-files`/`diff-content`
が共用する `deny[]`（type ごとに使えるキーが異なる）も同様にエラーになります。`type` ごとに
使えるキーは各検査のページ、または `spotter checks --json` を参照してください。command 型
（`types.<type>.command` を登録した command 型の検査）のオプションはこの検証の対象外で、
従来どおり `types.<type>.schema` で検証されます。

設定が構文として正しくても、リポジトリの実情と噛み合わなくなっていないか
（`doc-sync.pairs` が指すコードが無くなった、`types` に登録したのにどの `checks` からも
使われていない等）を確認したい場合は `spotter config lint` を使ってください
（`internal/confighygiene` に実装があります）。

正規表現で抽出する検査（`consistency` など）の `extract` / `line` / `until` を調整するときは
`spotter config explain <検査名>` で、検査が判定に使う抽出結果（ブロックの行範囲、抽出した
要素と出現位置）を合否を付けずに確認できます。0 件・ブロックの終端が無いなど、
`spotter check` なら実行エラーになる状況も、どこまで見たかを添えて正常終了します。終了コードに
合否は反映しません。対応する type は `spotter checks --json` の `explain_supported` で分かります。
合否そのものは、worktree 粒度の検査なら `spotter check <検査名>` をフラグ無しで走らせると
作業ツリーで確かめられます。`--json` で機械可読な出力にもできます。

- [doc-sync](checks/doc-sync.md): `pairs`（`paths`/`doc`/`when`/`on`/`doc_when`/`exclude`）, `exclude`
- [unwanted-files](checks/unwanted-files.md): `max_bytes`, `deny`（`paths`/`reason`）
- [doc-paths](checks/doc-paths.md): `docs`, `ignore`, `path_prefixes`
- [commit-subject](checks/commit-subject.md): `allowed_types`
- [consistency](checks/consistency.md): `sources`（`file`/`line`/`until`/`extract`/`split`/`subset`/`glob`/`base`/`exclude`）
- [diff-content](checks/diff-content.md): `deny`（`pattern`/`reason`/`on`/`paths`/`net`）
- [commit-intent](checks/commit-intent.md): `rules`（`types`/`scopes`/`breaking`/`allow`/`require`/`deny`/`deny_diff`/`on`/`reason`）
- [companion-files](checks/companion-files.md): `companions`（`paths`/`companion`/`reason`/`exclude`）
- [doc-links](checks/doc-links.md): `docs`, `ignore`, `check_anchors`
- [diff-size](checks/diff-size.md): `max_files`, `max_lines`, `exclude`
- [config-guard](checks/config-guard.md): 固有のフィールドは無し（`type` 以外を書くと
  起動時エラー）
- [command 型](checks/command.md): `types.<type>.schema` で宣言したオプション

`doc-paths` と `doc-links` は `docs`/`ignore` のキーを共用します（意味も同じ：対象
ドキュメントの一覧と、無視する候補・リンク先の完全一致リスト）。`path_prefixes` は
`doc-paths` 専用で必須です（末尾の `/` を付けた形式でも同じく許容されます）、
`check_anchors` は `doc-links` 専用です。`diff-size` の `exclude` は
`doc-sync` と同じキー（集計・比較から除外する doublestar パターンの一覧）を共用します。

`unwanted-files` と `diff-content` はどちらも `deny` キーを使いますが、要素の形が異なります
（`unwanted-files` は `paths`/`reason` が必須で `pattern`/`on`/`net` を指定できない、
`diff-content` は `pattern`/`reason` が必須）。誤って他方のフィールドを指定すると起動時
エラーになります。`net` はさらに `diff-content` の中でも `on: removed` のルールにしか
指定できません（[diff-content](checks/diff-content.md) 参照）。

同じ組み込み type を複数のキーでインスタンス化することもできます（例:
`doc-sync-frontend` と `doc-sync-backend` を別々の `pairs` で）。免除トレーラの既定名は
type ではなく**キー名**から生成されるため、この場合でもトレーラ名は衝突しません。

## `types`

次の**どちらか一方**の意味を持ちます（両方を同時に満たすことはできません）。

### 1. 組み込み type の `default` 上書き

```yaml
types:
  commit-subject:
    default:
      exempt:
        enable: false
```

組み込み type と同名のキーを置くと、その type 全体の既定値（現状は `exempt` のみ）を
上書きできます。`command` / `schema` / `transport` / `default.granularity` はここでは
指定できません（組み込み type は Go 側で挙動が固定されているため）。

### 2. command 型の登録

```yaml
types:
  my-check:
    command: bash             # 実行ファイル
    args: [scripts/my-check.sh]  # command に続けて渡す固定引数（省略可）
    transport: file          # file（既定） | args | env
    schema:
      simple: { ... }        # または json-schema
    default:
      granularity: squashed  # squashed | per-commit | worktree（必須）
```

組み込み type と衝突しない名前を使う場合、`command` の登録として扱われます。
`default.granularity` は必須です。詳細は [checks/command.md](checks/command.md) を
参照してください。

## YAML に正規表現を書くときの注意

`pattern`・`extract`・`when`・`doc_when` など、正規表現を取るキーは YAML の
クォート規則の影響を受けます。次の書き方が安全です。

- 正規表現は**シングルクォート**で書く。シングルクォートの中ではバックスラッシュが
  そのまま正規表現に渡ります（`'^feat\(.+\):'`）
- シングルクォートの中の `'` は `''` と重ねる（`'track[(]''([a-z_]+)'''` は
  `track[(]'([a-z_]+)'` になります）
- `\(` や `\.` の代わりに文字クラス `[(]`・`[.]` を使うと、バックスラッシュを避けられて
  読みやすくなります
- ダブルクォートで書くなら、バックスラッシュを二重にする（`"track\\('([a-z_]+)'"`）。
  `"track\('"` のように 1 つだけ書くと YAML が `unknown escape character` で読めません
- `|` や `>` のブロックスカラーは、末尾の改行やインデントが正規表現に混ざって意図せず
  一致しなくなることがあるため、正規表現には使わないでください

```yaml
extract: 'track[(]''([a-z_]+)'''
```

## `required_version`

```yaml
required_version: v0.1.0
```

このバージョン未満の `spotter` バイナリには検査を実行させません。詳細は
[versioning.md](versioning.md) を参照してください。省略可能です。

## `exempt` の解決順

`checks.<key>.exempt` の `enable`/`trailer` は、それぞれ次の優先順位でフォールバックします
（後段が指定されていれば優先）。

1. システム既定（`commit-subject` のみ無効、他は有効。トレーラ名はキーから自動生成）
2. `types.<type>.default.exempt`
3. `checks.<key>.exempt`

[config-guard](checks/config-guard.md) はこの解決順を実行時の設定にではなく、
比較元の `.spotter.yml` に対して当てはめる例外です。詳しくは
[exemptions.md](exemptions.md) を参照してください。
