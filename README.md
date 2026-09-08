# qwen

A single executable for asking Qwen questions through your llama.cpp server.
The receiving machine needs no Python, Go installation, GPU, or model files.

## Install

Install the latest release:

```sh
curl -fsSL https://github.com/mitchwolfe1/qwen-cli/releases/latest/download/install.sh | sh
```

The installer detects Linux x86-64, Linux ARM64, or Apple Silicon macOS,
downloads the matching binary, verifies its SHA-256 checksum, and installs it
to `/usr/local/bin/qwen`. It uses sudo for the installation step if needed.
Run the same command again to update to the latest release.

For installation without sudo:

```sh
curl -fsSL https://github.com/mitchwolfe1/qwen-cli/releases/latest/download/install.sh | QWEN_INSTALL_DIR="$HOME/.local/bin" sh
```

Put your chosen install directory on PATH. Check the installed release with
`qwen --version`.

### Manual download

Copy the matching executable from the [latest release](https://github.com/mitchwolfe1/qwen-cli/releases/latest):

| Machine | File |
| --- | --- |
| Linux x86-64 | [qwen-linux-amd64](https://github.com/mitchwolfe1/qwen-cli/releases/latest/download/qwen-linux-amd64) |
| Linux ARM64 | [qwen-linux-arm64](https://github.com/mitchwolfe1/qwen-cli/releases/latest/download/qwen-linux-arm64) |
| macOS, Apple Silicon | [qwen-darwin-arm64](https://github.com/mitchwolfe1/qwen-cli/releases/latest/download/qwen-darwin-arm64) |

Rename it to `qwen` during installation. For example, on Linux x86-64:

```sh
sudo mkdir -p /usr/local/bin
sudo install -m 755 qwen-linux-amd64 /usr/local/bin/qwen
```

Use your platform's filename.
The Linux executables are statically linked. Each executable targets only its
listed OS and CPU architecture.

The Linux x86-64 build was tested against Athena in both modes, including piped
input and a server URL override, with no Python or Go on PATH. The Linux ARM64
and Apple Silicon macOS builds were cross-compiled and their executable formats
checked; they have not been run on their target machines here.

## Use

```sh
qwen "How do I sort a Python dictionary?"
qwen -t "Why does this async code deadlock?"
qwen -n 100 "Explain Python slicing"
git diff | qwen -t "Check this for bugs"
printf 'Explain Python slicing' | qwen
qwen "Explain this" > answer.txt
```

Default mode uses temperature 0.7, top-p 0.8, and presence penalty 1.5.
`-t` uses the thinking/coding preset: temperature 0.6, top-p 0.95, and presence
penalty 0. Both use top-k 20, min-p 0, and repeat penalty 1.

Only the answer streams to stdout. Thinking status and errors go to stderr.
The model's reasoning trace is not printed. Without `-n`, the output limits are
4,096 tokens normally and 8,192 with thinking; reaching these default limits is
reported as an incomplete answer. Ctrl-C cancels the request.

### Exact token count

Use `-n N` to request exactly N generated tokens. `-n100` and `-n=100` also work.
The CLI automatically disables early end-of-sequence stopping and clears stop
strings. Reaching the requested count is a successful completion, even if the
answer is cut off mid-sentence; the server's reported token count is checked.

The count includes reasoning tokens with `-t`, so a small count may be spent
entirely on hidden thinking. If no answer text is produced, the CLI reports that
on stderr and exits nonzero. These are model-generated tokens, not words or a
retokenization of the printed answer; the CLI may append a terminal newline.
Context limits, transport errors, cancellation, and the five-minute request
timeout can still interrupt generation. Suppressing early stops may also make
the model continue awkwardly after it has finished its answer.

## Server address

The compiled default is **http://athena:8080**, with model alias `qwen3.5`.
The destination machine must be able to resolve `athena` and reach port 8080.
Override the address when needed:

```sh
QWEN_URL=http://192.168.1.10:8080 qwen "Hello"
```

To keep the override, add `export QWEN_URL=http://192.168.1.10:8080` to your shell
configuration. `QWEN_URL` also accepts a URL ending in `/v1` or the full
`/v1/chat/completions` endpoint. HTTP and HTTPS are supported.

## Build

Requires Go 1.23 or newer on the build machine. No third-party Go modules are used.

```sh
git clone git@github.com:mitchwolfe1/qwen-cli.git
cd qwen-cli
./build.sh
```

The script runs tests and vet, builds all three executables, copies `install.sh`,
and writes SHA-256 checksums to `dist/SHA256SUMS`. Set `GO=/path/to/go` to choose
a compiler, and `VERSION=v0.1.0` to embed a release version.

## Publish a release

After committing changes, choose a new version and build:

```sh
VERSION=v0.1.1 ./build.sh
git tag v0.1.1
git push origin main v0.1.1
gh release create v0.1.1 --repo mitchwolfe1/qwen-cli --verify-tag --draft \
  --title v0.1.1 --generate-notes \
  dist/qwen-linux-amd64 dist/qwen-linux-arm64 dist/qwen-darwin-arm64 \
  dist/install.sh dist/SHA256SUMS
gh release edit v0.1.1 --repo mitchwolfe1/qwen-cli --draft=false --latest
```

All assets are uploaded before publishing the release. The installer resolves
the latest release once and pins the binary and checksums to that same tag.
