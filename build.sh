#!/bin/sh
set -eu
cd "$(dirname "$0")"

if [ -n "${GO:-}" ]; then
    qwen_go=$GO
elif command -v go >/dev/null 2>&1; then
    qwen_go=go
else
    qwen_go="$HOME/.cache/qwen-go/go1.27.1/bin/go"
fi

qwen_version=${VERSION:-dev}
case "$qwen_version" in
    ''|*[!A-Za-z0-9._-]*) printf 'Invalid VERSION\n' >&2; exit 1 ;;
esac

"$qwen_go" test ./...
"$qwen_go" vet ./...
sh -n install.sh
mkdir -p dist

for target in linux/amd64 linux/arm64 darwin/arm64; do
    qwen_os=${target%/*}
    qwen_arch=${target#*/}
    CGO_ENABLED=0 GOOS="$qwen_os" GOARCH="$qwen_arch" GOAMD64=v1 GOARM64=v8.0 \
        "$qwen_go" build -trimpath -ldflags="-s -w -X main.version=$qwen_version" -o "dist/qwen-$qwen_os-$qwen_arch" .
done

cp install.sh dist/install.sh
chmod +x dist/install.sh
(
    cd dist
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum qwen-* install.sh
    else
        shasum -a 256 qwen-* install.sh
    fi
) > dist/SHA256SUMS
