#!/usr/bin/env bash
# sync-upstream.sh 把上游（原作者仓库）的新提交合并进本 fork。
#
# 背景：本仓库是 linguo2625469/workbuddy2api-panel 的 fork，origin 指向自己的
# fork（che246/workbuddy2api-panel），upstream 指向原作者仓库。本地提交分两类：
#   - 自己的改动（探活题面随机化、版本号 bump 等）；
#   - 上游合并进来的提交。
# 上游更新时用本脚本合并，避免手工 fetch/merge 时漏掉 tag 同步或忘记跑测试。
#
# 用法：
#   bash scripts/sync-upstream.sh          # 预演：只 fetch + 展示差异，不动工作区
#   bash scripts/sync-upstream.sh --merge  # 真正合并并推送到 origin
#
# 设计取舍：
#   - 默认预演（不带 --merge 不写任何东西）——合并是有冲突风险的操作，
#     先看清楚上游动了哪些文件再决定。
#   - 用 merge 而非 rebase：本 fork 已推送、已有 tag 与 Release，rebase 会
#     改写历史，让已发布的 tag 指向不存在的提交。
#   - 合并前工作区必须干净（未提交改动会让 merge 中途失败，且难回滚）。
#   - 合并后跑 go build + go vet + go test，全过才推送——上游可能改了测试
#     或引入与本 fork 冲突的假设，不验证就推等于把坏代码发出去。
set -euo pipefail

cd "$(dirname "$0")/.."

UPSTREAM_URL="https://github.com/linguo2625469/workbuddy2api-panel.git"
DO_MERGE=0
[ "${1:-}" = "--merge" ] && DO_MERGE=1

# 1. 确认 upstream remote 存在（首次使用自动加）
if ! git remote get-url upstream >/dev/null 2>&1; then
  echo "== 添加 upstream remote: $UPSTREAM_URL"
  git remote add upstream "$UPSTREAM_URL"
fi

# 2. 工作区必须干净
if [ -n "$(git status --porcelain)" ]; then
  echo "!! 工作区有未提交改动，先提交或 stash："
  git status --short
  exit 1
fi

# 3. 取上游（分支 + tag；--prune 清掉上游删掉的分支）
echo "== fetch upstream"
git fetch upstream --prune --tags

# 4. 差异概览
BEHIND=$(git rev-list --count main..upstream/main)
AHEAD=$(git rev-list --count upstream/main..main)
echo
echo "== 上游新提交 $BEHIND 个 / 本 fork 独有 $AHEAD 个"
if [ "$BEHIND" -eq 0 ]; then
  echo "上游无新提交，无需合并。"
  exit 0
fi

echo
echo "-- 上游新增提交 --"
git log --oneline main..upstream/main
echo
echo "-- 上游改动的文件 --"
git diff --stat main...upstream/main

if [ "$DO_MERGE" -eq 0 ]; then
  echo
  echo "== 预演结束。确认无误后执行：bash scripts/sync-upstream.sh --merge"
  exit 0
fi

# 5. 合并（--no-edit 用默认 merge 消息；冲突则停下让用户处理）
echo
echo "== merge upstream/main"
if ! git merge --no-edit upstream/main; then
  echo
  echo "!! 合并冲突。处理方式："
  echo "   1. 编辑冲突文件（git status 看 'both modified'）"
  echo "   2. git add <文件>"
  echo "   3. git commit"
  echo "   4. 重跑：go build ./... && go test ./..."
  echo "   放弃本次合并：git merge --abort"
  exit 1
fi

# 6. 合并后验证
echo
echo "== go build / go vet / go test"
go build ./... || { echo "!! build 失败，修复后再推"; exit 1; }
go vet ./... || { echo "!! vet 失败，修复后再推"; exit 1; }
go test ./... || { echo "!! test 失败，修复后再推"; exit 1; }

# 7. 推送
echo
echo "== push origin main"
git push origin main

echo
echo "== 完成。上游 $BEHIND 个提交已合并并推送。"
echo "   若上游带了新版本号，记得核对 cmd/server/main.go 的 appVersion 再决定是否发 Release。"
