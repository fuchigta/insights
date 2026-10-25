# commit-subject

コミットメッセージの 1 行目（subject）が [Conventional Commits](https://www.conventionalcommits.org/)
形式に合っているかを検証する検査です。

起動粒度は [per-commit](../granularity.md) 固定です。

## 設定

```yaml
checks:
  commit-subject:
    type: commit-subject
    allowed_types: [feat, fix, perf, refactor, docs, test, build, ci, chore, revert]
```

### `allowed_types`（必須）

許可する type の一覧。空にはできません（起動時にエラーになります。「常に不一致」という
無意味な検査を作らせないためのガードです）。

## 検証するパターン

```
<type>(<scope>)!: <description>
```

- `type` は `allowed_types` のいずれかと完全一致
- `(<scope>)` は省略可。中身は英数字と `._/-` のみ
- `!` は破壊的変更の印。省略可
- `: ` の後に 1 文字以上の説明が必須

subject が空、または `git revert` が生成する `Revert "<元の subject>"`（ダブルクォート
付き）の形の場合は検証対象から除外されます。「`Merge ` で始まる」は判定しません。
マージコミット自体は手元・CI どちらでも別の仕組みで検査対象から外れており
（[hooks.md](../hooks.md) の「マージコミットは検査しない」、
[granularity.md](../granularity.md) の range の除外）、ここで特別扱いすると
`Merge stuff` のような普通のコミットの subject 検証まですり抜けてしまうためです。
`Revert stuff` のようにダブルクォートを伴わない subject も、通常どおり検証されます。

## 免除は既定で無効

`commit-subject` は他の検査と異なり、**既定で免除トレーラが無効**です
（`enable: false` が type ごとの既定値）。理由は、この検査自体がメッセージの体裁そのものを
検証しているため、免除トレーラで通してしまうと「体裁の検証を体裁で回避する」矛盾が
起きるからです。有効化したい場合は明示してください。

```yaml
checks:
  commit-subject:
    type: commit-subject
    allowed_types: [feat, fix]
    exempt:
      enable: true
```

## この検査を他とどう噛み合わせるか

`allowed_types` は、リリースノート生成（[git-cliff](https://github.com/orhun/git-cliff) の
`commit_parsers` など）で使う type 一覧と重複しがちです。二重管理でずれるのを防ぎたい場合は
[consistency](consistency.md) 検査で両者を突き合わせる構成が使えます
（このリポジトリの `.spotter.yml` 自体がその実例です）。

`commit-subject` が見るのは**体裁**（subject の形として正しいか）だけです。「`docs:` と
名乗りながらコードを書き換えている」のような、**申告した type と実際の差分の乖離**を検知
したい場合は [commit-intent](commit-intent.md) を使ってください。
