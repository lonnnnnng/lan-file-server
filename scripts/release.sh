#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repo_dir"
release_version="$(tr -d '\r\n' < VERSION)"
release_commit="$(git rev-parse HEAD)"
release_time="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
[[ "$release_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'VERSION 必须是 x.y.z' >&2; exit 1; }
[[ -z "$(git status --porcelain --untracked-files=normal)" ]] || { echo '发布前必须提交所有源码改动。' >&2; exit 1; }
mkdir -p dist
stage=""
cleanup() {
  case "$stage" in
    "$repo_dir"/dist/.stage-*) [[ ! -d "$stage" ]] || rm -rf -- "$stage" ;;
  esac
}
trap cleanup EXIT

for arch in amd64 arm64; do
  stage="$(mktemp -d "$repo_dir/dist/.stage-$arch.XXXXXX")"
  mkdir -p "$stage/lan-file-server/bin"
  git archive HEAD | tar -x -C "$stage/lan-file-server"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=$release_version -X main.commit=$release_commit -X main.buildTime=$release_time" \
    -o "$stage/lan-file-server/bin/lan-file-server" .
  tar -czf "dist/lan-file-server_${release_version}_linux_${arch}.tar.gz" -C "$stage" lan-file-server
  cleanup
  stage=""
done

cd dist
shasum -a 256 "lan-file-server_${release_version}_linux_amd64.tar.gz" "lan-file-server_${release_version}_linux_arm64.tar.gz" > SHA256SUMS
printf 'Release assets ready: dist/ (version %s, commit %s)\n' "$release_version" "$release_commit"
