#!/bin/sh
set -eu

repo="${REVOPIA_REPO:-yewfence/revopia}"
version="${VERSION:-${REVOPIA_VERSION:-}}"

if [ -n "${REVOPIA_INSTALL_PATH:-}" ]; then
  install_path="$REVOPIA_INSTALL_PATH"
else
  install_path="./revopia"
fi

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

github_curl() {
  accept="$1"
  shift
  curl -fsSL \
    -H "Accept: $accept" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "$@"
}

need curl
need jq

if command -v sha256sum >/dev/null 2>&1; then
  verify_sha256() {
    echo "$1  $2" | sha256sum -c -
  }
elif command -v shasum >/dev/null 2>&1; then
  verify_sha256() {
    echo "$1  $2" | shasum -a 256 -c -
  }
else
  echo "missing required command: sha256sum or shasum" >&2
  exit 1
fi

arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) artifact_arch=amd64 ;;
  aarch64 | arm64) artifact_arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

asset_name="revopia-linux-$artifact_arch"
if [ -n "$version" ]; then
  release_url="https://api.github.com/repos/$repo/releases/tags/$version"
else
  release_url="https://api.github.com/repos/$repo/releases/latest"
fi

release="$(github_curl "application/vnd.github+json" "$release_url")" || {
  echo "failed to fetch release metadata: $release_url" >&2
  exit 1
}
asset="$(printf '%s' "$release" | jq -er --arg name "$asset_name" '.assets[] | select(.name == $name)')" || {
  echo "release asset not found: $asset_name" >&2
  exit 1
}
download_url="$(printf '%s' "$asset" | jq -er '.url')"
digest="$(printf '%s' "$asset" | jq -er '.digest | strings | select(startswith("sha256:")) | sub("^sha256:"; "")')" || {
  echo "release asset sha256 digest not found: $asset_name" >&2
  exit 1
}

install_dir="$(dirname "$install_path")"
tmp_path="$install_dir/.revopia.tmp.$$"
mkdir -p "$install_dir"
trap 'rm -f "$tmp_path"' EXIT INT TERM

github_curl "application/octet-stream" "$download_url" -o "$tmp_path"
verify_sha256 "$digest" "$tmp_path"
chmod 0755 "$tmp_path"
mv "$tmp_path" "$install_path"
trap - EXIT INT TERM

echo "revopia installed to $install_path"
