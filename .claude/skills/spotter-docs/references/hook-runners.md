# 他のフックランナーとの共存（lefthook / husky / pre-commit）

[hooks.md](hooks.md) の「既存のフックファイルがあるとき」のとおり、`spotter hooks
install` は spotter の管理ブロックを含まない既存のフックファイルには書き込みません。
lefthook・husky・pre-commit のフックファイルはどれも spotter の管理ブロックを持たない
ため、`spotter hooks install` を向けても書き込まれず、呼び出し行の案内が出るだけです。
ここでは lefthook・husky v9・pre-commit（Python 版）それぞれについて、実機で確かめた
上でのレシピをまとめます。

いずれのツールでも、呼び出し行そのものは `spotter hooks install --print` で確認できます。

```bash
spotter hooks install --print
# commit-msg: spotter check --message "$1"
# pre-push: spotter check --pre-push "$1"
```

## lefthook

`pre-push` フックそのものは lefthook が管理し、`spotter hooks install` は使わずに
`--print` の出力を lefthook の設定にそのまま貼り付けます。lefthook の `run:` はフック引数を
`{1}`（`"$1"` ではありません）で受け取ります。

```yaml
commit-msg:
  commands:
    spotter:
      run: spotter check --message {1}

pre-push:
  commands:
    spotter:
      run: spotter check --pre-push {1}
      use_stdin: true
```

**`use_stdin: true` が必須です。** lefthook は既定では pre-push フックの標準入力をコマンドに
渡しません。付け忘れると `spotter check --pre-push` に ref の並びが 1 行も届かず、
`pre-push が検査する範囲`（[hooks.md](hooks.md)）に書いたとおり検査されずに理由だけが
stderr に出ます。

### `spotter hooks install` を向けても書き込まれない

lefthook がインストールした `.git/hooks/commit-msg` や `.git/hooks/pre-push` は
`call_lefthook run "<hook>" "$@"` の呼び出しで終わる、spotter の管理ブロックを持たない
ファイルです。`spotter hooks install` を実行してもこれらには書き込まれず（`foreign` として
報告され、呼び出し行の案内が出るだけです）、lefthook の設定は影響を受けません。lefthook を
使う場合は、上記のように呼び出し行を lefthook の設定側に組み込んでください。

## husky v9

husky v9 のフックファイル（`.husky/<hook名>`）は、シェバンや `husky.sh` の読み込みを必要と
しないプレーンなシェルスクリプトです。`--print` の出力をそのままファイルの中身にします。

```bash
echo 'spotter check --message "$1"' > .husky/commit-msg
echo 'spotter check --pre-push "$1"' > .husky/pre-push
```

標準入力・引数は husky の内部スクリプト（`.husky/_/h`）がそのまま引き継ぐため、追加の設定は
不要です。

### `spotter hooks install` は解決先が違う

husky は `core.hooksPath` を `.husky/_` に設定します。`spotter hooks install` はこの値を
尊重してフックファイルを解決しますが、解決先は `.husky/_/<hook名>`（husky が生成する内部の
中継スクリプト）であり、利用者が編集する `.husky/<hook名>` ではありません。この中継
スクリプトは spotter の管理ブロックを持たないため、`spotter hooks install` を実行しても
書き込まれず（`foreign` として報告され、呼び出し行の案内が出るだけです）、案内された
呼び出し行はそこではなく上記のように `.husky/<hook名>` に直接書いてください。

## pre-commit（Python 版）

### commit-msg

commit-msg はメッセージファイルのパスを引数で受け取るだけなので、そのまま local hook に
組み込めます。

```yaml
repos:
  - repo: local
    hooks:
      - id: spotter-commit-msg
        name: spotter (commit-msg)
        language: system
        entry: spotter check --message
        stages: [commit-msg]
```

### pre-push の限界

pre-commit の `hook_impl` は pre-push フックの標準入力を自分で読み切り、各フックには渡さず
`PRE_COMMIT_FROM_REF`（remote sha）・`PRE_COMMIT_TO_REF`（local sha）・
`PRE_COMMIT_LOCAL_BRANCH`・`PRE_COMMIT_REMOTE_BRANCH`・`PRE_COMMIT_REMOTE_NAME` という環境変数に
変換します。これらから `<local ref> <local sha> <remote ref> <remote sha>` の 1 行を組み立てて
`spotter check --pre-push` に渡すことはでき、単一 ref の通常の push（既存ブランチの更新・
共有履歴のある新規ブランチ）では実機で正しく動作することを確認しています。

```yaml
      - id: spotter-pre-push
        name: spotter (pre-push)
        language: system
        entry: sh -c 'printf "%s %s %s %s\n" "$PRE_COMMIT_LOCAL_BRANCH" "$PRE_COMMIT_TO_REF" "$PRE_COMMIT_REMOTE_BRANCH" "$PRE_COMMIT_FROM_REF" | spotter check --pre-push "$PRE_COMMIT_REMOTE_NAME"'
        stages: [pre-push]
        pass_filenames: false
        always_run: true
```

ただし実機で次の 2 つの限界を確認しており、この組み方は推奨しません。

- **複数 ref を同時に push すると、検査されるのは 1 ref だけです。** `git push origin
  main feature` のように複数 ref を同時に push しても、pre-commit は pre-push フックを
  1 回しか呼ばず、環境変数は最後に処理された 1 ref 分の値のままです。他の ref は検査されずに
  push されます（`spotter check --pre-push` 自身は複数 ref を渡されれば ref ごとに検査します
  が、そもそも pre-commit からその ref の情報が渡ってきません）。
- **共有履歴の無い新規ブランチ（orphan ブランチ）では `PRE_COMMIT_TO_REF` /
  `PRE_COMMIT_FROM_REF` が空になります。** 組み立てた行が `<local ref>  <remote ref> `
  のような不正な形になり、`spotter check --pre-push` はこれを解釈できずにエラーで
  push を止めます（黙って素通りするわけではありませんが、意図と異なるエラーで止まります）。

### 推奨: pre-push は pre-commit に持たせない

pre-commit は `core.hooksPath` を設定せず、`--hook-type` で指定したフックだけを
`.git/hooks/` に書きます。commit-msg だけを pre-commit にインストールし（`pre-commit
install --hook-type commit-msg`）、pre-push は `spotter hooks install --hook pre-push` で
別に設置すると、既定の hooks ディレクトリに pre-commit 以外の既存フックが無い状態のまま
`.git/hooks/pre-push` に spotter 用のフックが新規作成されます。この構成なら pre-push フックは
git が渡す標準入力をそのまま受け取るため、複数 ref の同時 push も orphan ブランチも
`spotter check --pre-push` 自身の規則（[hooks.md](hooks.md) の「pre-push が検査する範囲」）
どおりに扱われることを実機で確認しています。

**pre-commit で `pre-push` フックを既にインストールしている場合はこの手順は使えません。**
pre-commit が生成する `.git/hooks/pre-push`・`.git/hooks/commit-msg` は
`exec "$INSTALL_PYTHON" -mpre_commit ...`（または `exec pre-commit ...`）で終わっており、
spotter の管理ブロックを持ちません。`spotter hooks install --hook pre-push` を実行しても
このファイルには書き込まれず（`foreign` として報告され、呼び出し行の案内が出るだけです）、
案内に従って呼び出し行を pre-commit の pre-push 設定（上記の `entry` のシェル）側に
組み込んでください。ただし「pre-push の限界」で述べた複数 ref・orphan ブランチの制約は
そのまま残ります。
