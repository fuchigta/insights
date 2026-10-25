---
name: spotter-docs
description: spotter（git commit 前と CI の両方で同じ検査を実行する CLI）のドキュメントをオフラインで参照する。.spotter.yml の書き方、組み込み検査 type（doc-sync, unwanted-files, doc-paths, commit-subject, consistency, diff-content, commit-intent, companion-files, doc-links, diff-size, config-guard）のオプション、免除トレーラ、起動粒度、commit-msg / pre-push フックの設置、CI 連携、command 型の作り方を調べるときに使う。
license: MIT
metadata:
    managed-by: spotter
    spotter-content-hash: 8e7f2343fafe7900cfd62230f892e05ad3b9a2d5e95ec749a85dee85e0800778
    spotter-version: v0.13.0
---

# spotter ドキュメント

`references/` 配下は spotter リポジトリの `docs/` をそのまま写したものです。
全部読む必要はありません。知りたいことに応じて次の表から 1〜2 ファイルだけ選んで
読んでください。

このスキルの中身は spotter のリリースに同梱されています。手元の `spotter --version`
と内容が食い違っていそうなら、最新版の spotter を取得し直してからこのスキルを
入れ直してください。

**`references/` 配下が見つからない場合**（`spotter skills install` ではなく Claude Code
のプラグイン機構経由でこのスキルを入れた場合に起こります。`references/` はインストール
時に spotter バイナリが `docs/` から合成するもので、プラグイン経由だと合成されません）は、
このスキルと同じリポジトリのルートにある `docs/` を直接探してください。パスの対応は
`references/` を `docs/` に読み替えるだけです（例: `references/checks/doc-sync.md` →
`docs/checks/doc-sync.md`）。

## まず読むもの

- `.spotter.yml` 全体の構造（`checks` / `types` / `required_version` の 3 キー）を
  知りたい → `references/config-reference.md`
- 組み込み検査の一覧・各 type の設定キーを機械的に確認したい →
  リポジトリ内で `spotter checks --json` を実行する（このスキルの散文より正確）

## 検査 type ごとの詳細（`references/checks/`）

| 知りたいこと | 読むファイル |
|---|---|
| コードとドキュメントの対応漏れ検知 | `references/checks/doc-sync.md` |
| コミットしてはいけないファイルの混入防止 | `references/checks/unwanted-files.md` |
| ドキュメントが指すコードパスの実在確認 | `references/checks/doc-paths.md` |
| Conventional Commits 形式の検証 | `references/checks/commit-subject.md` |
| 複数ファイルから抽出した集合の一致検証 | `references/checks/consistency.md` |
| 差分の追加/削除行への deny パターン | `references/checks/diff-content.md` |
| コミットの type/scope と変更内容の整合 | `references/checks/commit-intent.md` |
| 相方ファイル（テストなど）の存在確認 | `references/checks/companion-files.md` |
| Markdown のリンク切れ検出 | `references/checks/doc-links.md` |
| 1 コミットの変更量の上限 | `references/checks/diff-size.md` |
| `.spotter.yml` 自体の変更が他の検査を緩めていないかの検証 | `references/checks/config-guard.md` |
| 外部コマンドで独自の検査を追加する | `references/checks/command.md` |

## 仕組みの背景

- spotter が利用者に約束していること（手元と CI で結果が一致する、免除には理由が要る
  など）と、その根拠 → `references/principles.md`
- 検査ごとの起動粒度（squashed / per-commit / worktree）とその理由 →
  `references/granularity.md`
- コミットメッセージのトレーラによる免除の仕組みと運用 → `references/exemptions.md`

## 運用

- `spotter hooks install`（commit-msg / pre-push フックの設置）の挙動、pre-push が検査する
  範囲 → `references/hooks.md`
- lefthook / husky / pre-commit との共存レシピ → `references/hook-runners.md`
- `spotter update`（バイナリ自体の更新）の挙動 → `references/update.md`
- `spotter range` による CI 側の範囲自動検出 → `references/ci-integration.md`
- `required_version` によるバイナリバージョンの固定 → `references/versioning.md`
- `spotter skills`（このスキル自身を含む、コーディングエージェント向けスキルの
  設置・削除・状態確認）のターゲット・スコープ・冪等性の仕組み →
  `references/skills.md`

## 実際に検証する

ドキュメントを読んだだけで `.spotter.yml` を書き換えないでください。変更したら
必ず次のどちらかで検証してから提示・コミットしてください。

```bash
spotter doctor --config .spotter.yml
spotter check --config .spotter.yml --range "-10 HEAD"
```

`consistency` / `doc-paths` / `doc-links` の抽出規則（`extract` / `line` / `until` /
`path_prefixes` など）を書いたら、`spotter config explain <検査名>` で、実際に何を
抽出するかも確認してください。
