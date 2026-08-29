#!/bin/sh
# Conventional Commits の subject 行を検証する。
# commit-msg フック（.githooks/commit-msg）と CI の両方から呼ばれる。
#
# 使い方:
#   scripts/check-commit-subject.sh "feat: 新機能を追加する"
#   git log --format=%s a..b | scripts/check-commit-subject.sh
set -eu

# type(scope)!: 説明
# scope は任意。! は破壊的変更。説明は 1 文字以上。
PATTERN='^(feat|fix|perf|refactor|docs|test|build|ci|chore|revert)(\([a-zA-Z0-9._/-]+\))?!?: .+'

status=0

check_one() {
  subject=$1
  # git が自動生成するマージ・リバートのメッセージは対象外にする。
  case "$subject" in
    "Merge "* | "Revert "* | "") return 0 ;;
  esac
  if printf '%s' "$subject" | grep -Eq "$PATTERN"; then
    return 0
  fi
  printf '不正なコミットメッセージです: %s\n' "$subject" >&2
  status=1
}

if [ "$#" -gt 0 ]; then
  for s in "$@"; do check_one "$s"; done
else
  while IFS= read -r s; do check_one "$s"; done
fi

if [ "$status" -ne 0 ]; then
  cat >&2 <<'MSG'

このリポジトリは Conventional Commits を使います。次の形式で書いてください。

  <type>[(scope)][!]: <説明>

  type: feat / fix / perf / refactor / docs / test / build / ci / chore / revert
  !  : 破壊的変更のとき付ける
  説明: 日本語でよい。何をしたかが分かるように書く

例:
  feat: HTML レポートに推移グラフを追加する
  fix(rollup): サブエージェントのコストが二重計上される問題を直す
  feat(judge)!: 評価スキーマの列挙値を変更する

リリースノートはこの type ごとに分類して自動生成されます（cliff.toml）。
MSG
fi

exit "$status"
