#!/usr/bin/env sh
# Install the ai-workflow binary from a GitHub release after SHA-256 verification.
# Usage: curl -fsSL https://raw.githubusercontent.com/Taki7980/Ai-workflow-V3/main/install.sh | sh
# Env: AI_WORKFLOW_VERSION=X.Y.Z (default latest), AI_WORKFLOW_INSTALL_DIR (default ~/.local/bin)
set -eu

REPO="Taki7980/Ai-workflow-V3"
INSTALL_DIR="${AI_WORKFLOW_INSTALL_DIR:-$HOME/.local/bin}"

if [ -n "${AI_WORKFLOW_VERSION:-}" ]; then
  VERSION="${AI_WORKFLOW_VERSION#v}"
else
  latest_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")"
  VERSION="${latest_url##*/v}"
fi
printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || {
  echo "Unable to resolve a stable release version (got: $VERSION)" >&2
  exit 1
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "Unsupported OS: $(uname -s); on Windows use install.ps1" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

asset="ai-workflow-v${VERSION}-${os}-${arch}"
base="https://github.com/${REPO}/releases/download/v${VERSION}"
if [ -n "${AI_WORKFLOW_TEST_RELEASE_BASE:-}" ]; then
  case "$AI_WORKFLOW_TEST_RELEASE_BASE" in
    http://127.0.0.1:*|http://localhost:*) base="$AI_WORKFLOW_TEST_RELEASE_BASE" ;;
    *) echo "AI_WORKFLOW_TEST_RELEASE_BASE is restricted to localhost HTTP" >&2; exit 1 ;;
  esac
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

curl -fsSL --proto '=https,http' "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
curl -fsSL --proto '=https,http' "$base/$asset" -o "$tmp/$asset"
expected="$(awk -v f="$asset" '$2 == f || $2 == "*"f {print $1}' "$tmp/SHA256SUMS")"
[ -n "$expected" ] || { echo "No SHA-256 checksum listed for $asset" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi
[ "$expected" = "$actual" ] || { echo "SHA-256 verification failed for $asset" >&2; exit 1; }

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/$asset" "$INSTALL_DIR/ai-workflow"

echo "Installed ai-workflow $VERSION to $INSTALL_DIR/ai-workflow (SHA-256 verified)"
case ":$PATH:" in *":$INSTALL_DIR:"*) ;; *) echo "Add $INSTALL_DIR to your PATH." ;; esac
echo "Verify build provenance: gh attestation verify \"$INSTALL_DIR/ai-workflow\" --repo $REPO"
