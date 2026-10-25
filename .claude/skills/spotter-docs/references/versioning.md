# `required_version` によるバージョン固定

```yaml
required_version: v0.1.0
```

`.spotter.yml` に検査を追加・変更したのに、手元の `spotter` バイナリが古いままだと
「新しい検査が走っていないだけなのに、手元では通ってしまう」状態になり得ます。これは
「手元で通ったものは CI でも通る」という `spotter` の前提を壊します。`required_version`
はこれを防ぐための下限バージョン指定です。

## 挙動

- `spotter check` / `spotter doctor` は、設定を読み込んだ直後にバージョンを検証します
- 手元のバイナリのバージョンが `required_version` 未満なら、**検査を一切実行せず**
  エラーで終了します
- バージョン文字列は `v1.2.3` 形式（`v` は省略可、末尾に `-rc1` のようなプレリリース
  識別子が付いていても無視して比較します）
- 比較は major/minor/patch の数値比較です（`v0.3.0` は `v0.2.9` より新しい、等）

## バージョンの決め方

バイナリが名乗るバージョンは `internal/version` パッケージの `Resolve` 関数が決めます。

1. `-ldflags -X main.version=...` で埋め込まれた値があればそれを使う（リリースバイナリ）
2. 埋め込まれていない（`dev` のままの）場合は、Go ツールチェインが `go install
   pkg@version` や `go run pkg@version` のようにモジュールとして取得したときに
   `runtime/debug.BuildInfo` へ刻む `Main.Version`（例: `v0.3.1`）を使う
3. それも無い（`go run ./cmd/spotter` のようにこのリポジトリのソースを直接実行した
   場合。`Main.Version` は空か `(devel)` になります）場合は `dev` のまま

git の作業ツリーでソースから `go build` した場合は、Go ツールチェインが VCS の情報から
擬似バージョン（例: `v0.8.1-0.20260924020940-2c601ba2a758`）を `Main.Version` に刻むため、
2 に当たります。擬似バージョンは末尾を無視して比較するので、直前のタグの次のパッチ版
（この例では `v0.8.1`）として扱われます。

したがって `go install github.com/fuchigta/spotter/cmd/spotter@v0.3.1` のように
タグを指定して入れたバイナリは、そのタグをバージョンとして名乗り、
`required_version` の判定も通常どおりかかります。`@latest` の場合も、解決された
タグのバージョンが刻まれます。

## dev のときは判定不能として素通りする

このリポジトリ自身のソースを `go run ./cmd/spotter` で実行した場合、バージョンは
`dev` になります。`dev` は正式なバージョン文字列として解釈できないため、
`required_version` の判定は**「判定不能」として常に満たしているとみなされます**
（エラーにはなりません）。

これは「ソースからビルドしているなら、少なくともそのソース時点の実装で動いている」という
前提に立った割り切りです。つまり `required_version` が実質的に意味を持たないのは
**ソースを直接ビルドしている環境**（このリポジトリ自身の commit-msg フックや CI が
そうです。[hooks.md](hooks.md) 参照）だけで、`go install` / `go run pkg@version` で
モジュールとして取得した環境では判定がかかります。

## 確認方法

```
spotter doctor
```

`required_version` を設定していれば、手元のバイナリがそれを満たしているかを表示します。
満たしていない場合は `doctor` 自体も失敗（非 0 終了）します。

満たしていない場合は `spotter update` でバイナリ自体を更新できます。詳しくは
[update.md](update.md) を参照してください（`go run` でソースを直接使っている環境では
`git pull` 等でソース自体を更新することになるため対象外です）。
