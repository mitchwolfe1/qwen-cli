#!/bin/sh
# Install the latest release from Mitch's personal GitHub repository.
set -eu

fail() {
    printf 'qwen installer: %s\n' "$*" >&2
    exit 1
}

run_install() {
    if [ "$qwen_use_sudo" = yes ]; then
        sudo "$@"
    else
        "$@"
    fi
}

cleanup() {
    if [ -n "${qwen_staging:-}" ]; then
        run_install rm -f "$qwen_staging"
    fi
    rm -rf "$qwen_tmp"
}

main() {
    qwen_repo=mitchwolfe1/qwen-cli
    qwen_dir=${QWEN_INSTALL_DIR:-/usr/local/bin}
    case "$qwen_dir" in
        /*) ;;
        *) fail 'QWEN_INSTALL_DIR must be an absolute path' ;;
    esac
    command -v curl >/dev/null 2>&1 || fail 'curl is required'

    case "$(uname -s)/$(uname -m)" in
        Linux/x86_64|Linux/amd64) qwen_asset=qwen-linux-amd64 ;;
        Linux/aarch64|Linux/arm64) qwen_asset=qwen-linux-arm64 ;;
        Darwin/arm64) qwen_asset=qwen-darwin-arm64 ;;
        Darwin/x86_64)
            if [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
                qwen_asset=qwen-darwin-arm64
            else
                fail 'macOS requires Apple Silicon; Intel Macs are not supported'
            fi
            ;;
        *) fail "unsupported platform: $(uname -s)/$(uname -m)" ;;
    esac

    if command -v sha256sum >/dev/null 2>&1; then
        qwen_hash=sha256sum
    elif command -v shasum >/dev/null 2>&1; then
        qwen_hash=shasum
    else
        fail 'sha256sum or shasum is required'
    fi

    qwen_use_sudo=no
    qwen_staging=
    qwen_tmp=$(mktemp -d "${TMPDIR:-/tmp}/qwen-install.XXXXXX")
    trap cleanup 0
    trap 'exit 130' INT
    trap 'exit 143' TERM
    trap 'exit 129' HUP

    # Resolve latest once, so an intervening release cannot mix assets/checksums.
    qwen_release=$(curl -fsSLI --proto '=https' --proto-redir '=https' \
        --retry 3 --connect-timeout 10 -o /dev/null -w '%{url_effective}' \
        "https://github.com/$qwen_repo/releases/latest")
    case "$qwen_release" in
        "https://github.com/$qwen_repo/releases/tag/"*) qwen_tag=${qwen_release##*/} ;;
        *) fail 'could not resolve the latest release' ;;
    esac
    case "$qwen_tag" in
        ''|*[!A-Za-z0-9._-]*) fail 'the release tag is invalid' ;;
    esac
    qwen_download="https://github.com/$qwen_repo/releases/download/$qwen_tag"
    printf 'Downloading qwen %s (%s)…\n' "$qwen_tag" "$qwen_asset" >&2
    curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 10 \
        "$qwen_download/$qwen_asset" -o "$qwen_tmp/$qwen_asset"
    curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 10 \
        "$qwen_download/SHA256SUMS" -o "$qwen_tmp/SHA256SUMS"

    qwen_expected=$(awk -v asset="$qwen_asset" '$2 == asset {print tolower($1)}' "$qwen_tmp/SHA256SUMS")
    [ "${#qwen_expected}" -eq 64 ] || fail 'the release has no valid checksum for this binary'
    case "$qwen_expected" in *[!0-9a-f]*) fail 'invalid SHA-256 checksum' ;; esac
    if [ "$qwen_hash" = sha256sum ]; then
        qwen_actual=$(sha256sum "$qwen_tmp/$qwen_asset" | awk '{print $1}')
    else
        qwen_actual=$(shasum -a 256 "$qwen_tmp/$qwen_asset" | awk '{print $1}')
    fi
    [ "$qwen_actual" = "$qwen_expected" ] || fail 'checksum mismatch; nothing was installed'

    if ! mkdir -p "$qwen_dir" 2>/dev/null || [ ! -w "$qwen_dir" ]; then
        command -v sudo >/dev/null 2>&1 || fail 'install directory is not writable; set QWEN_INSTALL_DIR to a user-writable directory'
        qwen_use_sudo=yes
        run_install mkdir -p "$qwen_dir"
    fi
    [ ! -d "$qwen_dir/qwen" ] || fail 'the destination qwen is a directory'
    qwen_staging=$(run_install mktemp "$qwen_dir/.qwen.XXXXXX")
    run_install install -m 755 "$qwen_tmp/$qwen_asset" "$qwen_staging"
    run_install mv -f "$qwen_staging" "$qwen_dir/qwen"
    qwen_staging=
    printf 'Installed qwen %s to %s/qwen\n' "$qwen_tag" "$qwen_dir" >&2
    case ":$PATH:" in
        *":$qwen_dir:"*) ;;
        *) printf 'Add %s to your PATH to run qwen.\n' "$qwen_dir" >&2 ;;
    esac
}

main "$@"
