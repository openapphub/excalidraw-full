#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
app_root="$repo_root/excalidraw/excalidraw-app"
build_dir="$app_root/build"
frontend_dir="$repo_root/frontend"
stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/excalidraw-frontend.XXXXXX")"

cleanup() {
  rm -rf "$stage_dir"
}
trap cleanup EXIT

if [[ ! -f "$app_root/package.json" ]]; then
  echo "找不到 Excalidraw 前端子模块：$app_root" >&2
  exit 1
fi

(
  cd "$app_root"
  DISABLE_VITE_CHECKER=true pnpm build:app:docker
)

if [[ ! -f "$build_dir/index.html" ]]; then
  echo "前端构建未生成 index.html：$build_dir" >&2
  exit 1
fi

rsync -a --exclude='*.map' "$build_dir/" "$stage_dir/"
find "$stage_dir" -type f -name '*.map' -delete

# frontend 是 go:embed 的专用生成目录；仅在成功完成暂存构建后替换它。
rm -rf "$frontend_dir"
mv "$stage_dir" "$frontend_dir"
touch "$frontend_dir/.keep"
trap - EXIT

echo "已更新 ${frontend_dir}（已排除 sourcemap）"
