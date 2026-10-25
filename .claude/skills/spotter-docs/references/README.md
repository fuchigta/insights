# ドキュメント目次

README.md はインストールとクイックスタートまでに留めています。個々の検査の詳細な設定や、
仕組みの背景にある考え方はここから辿ってください。

spotter が利用者に約束していること（手元で通ったものは CI でも通る、など）は
[principles.md](principles.md) にまとめています。

## 組み込み検査のリファレンス

| ドキュメント | 検査 type | 概要 |
|---|---|---|
| [checks/doc-sync.md](checks/doc-sync.md) | `doc-sync` | コードとドキュメントの対応漏れを検知する |
| [checks/unwanted-files.md](checks/unwanted-files.md) | `unwanted-files` | コミットしてはいけないものの混入を防ぐ |
| [checks/doc-paths.md](checks/doc-paths.md) | `doc-paths` | ドキュメントが名指しするパスの実在を確認する |
| [checks/commit-subject.md](checks/commit-subject.md) | `commit-subject` | Conventional Commits 形式を検証する |
| [checks/consistency.md](checks/consistency.md) | `consistency` | 複数ファイルから抽出した集合の一致を検証する |
| [checks/diff-content.md](checks/diff-content.md) | `diff-content` | 差分の追加/削除行に対する deny パターンを検証する |
| [checks/commit-intent.md](checks/commit-intent.md) | `commit-intent` | コミットの type/scope と実際の変更内容の整合を検証する |
| [checks/companion-files.md](checks/companion-files.md) | `companion-files` | 触ったファイルに対する相方ファイルの存在を検証する |
| [checks/doc-links.md](checks/doc-links.md) | `doc-links` | Markdown のリンク記法が指すファイルの実在（リンク切れ）を検証する |
| [checks/diff-size.md](checks/diff-size.md) | `diff-size` | 1 コミットの変更量（ファイル数・行数）に上限を設ける |
| [checks/config-guard.md](checks/config-guard.md) | `config-guard` | `.spotter.yml` 自体の変更が他の検査を緩めていないかを検証する |
| [checks/command.md](checks/command.md) | （`types` に登録） | 外部コマンドで独自の検査を追加する |

## 仕組みの背景

| ドキュメント | 内容 |
|---|---|
| [granularity.md](granularity.md) | 検査ごとの起動粒度（squashed / per-commit / worktree）とその理由 |
| [exemptions.md](exemptions.md) | コミットメッセージのトレーラによる免除の仕組みと運用 |

## 運用

| ドキュメント | 内容 |
|---|---|
| [hooks.md](hooks.md) | `spotter hooks install` の挙動、pre-push が検査する範囲 |
| [hook-runners.md](hook-runners.md) | lefthook / husky / pre-commit との共存レシピ |
| [update.md](update.md) | `spotter update` によるバイナリ自体の更新 |
| [skills.md](skills.md) | `spotter skills` によるコーディングエージェント向けスキルの設置・削除・状態確認 |
| [ci-integration.md](ci-integration.md) | `spotter range` による CI 側の範囲自動検出 |
| [config-reference.md](config-reference.md) | `.spotter.yml` 全体の構造リファレンス |
| [versioning.md](versioning.md) | `required_version` によるバイナリバージョンの固定 |
