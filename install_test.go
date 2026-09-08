package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstaller(t *testing.T) {
	for _, test := range []struct {
		name, system, arch, hardwareARM, asset string
		badChecksum, unsupported, shasumOnly   bool
	}{
		{name: "linux amd64", system: "Linux", arch: "x86_64", asset: "qwen-linux-amd64"},
		{name: "linux arm64", system: "Linux", arch: "aarch64", asset: "qwen-linux-arm64"},
		{name: "apple silicon", system: "Darwin", arch: "arm64", asset: "qwen-darwin-arm64"},
		{name: "rosetta", system: "Darwin", arch: "x86_64", hardwareARM: "1", asset: "qwen-darwin-arm64"},
		{name: "shasum fallback", system: "Darwin", arch: "arm64", asset: "qwen-darwin-arm64", shasumOnly: true},
		{name: "bad checksum", system: "Linux", arch: "x86_64", asset: "qwen-linux-amd64", badChecksum: true},
		{name: "intel mac", system: "Darwin", arch: "x86_64", hardwareARM: "0", unsupported: true},
		{name: "unsupported platform", system: "FreeBSD", arch: "x86_64", unsupported: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			shim := filepath.Join(root, "bin")
			fixture := filepath.Join(root, "release")
			target := filepath.Join(root, "install with spaces")
			for _, dir := range []string{shim, fixture, target} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(target, "qwen"), "previous binary")
			var sums strings.Builder
			for _, asset := range []string{"qwen-linux-amd64", "qwen-linux-arm64", "qwen-darwin-arm64"} {
				content := "fixture for " + asset
				write(filepath.Join(fixture, asset), content)
				fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256([]byte(content)), asset)
			}
			write(filepath.Join(fixture, "SHA256SUMS"), sums.String())
			if test.badChecksum {
				write(filepath.Join(fixture, test.asset), "tampered binary")
			}
			write(filepath.Join(shim, "uname"), "#!/bin/sh\ncase \"$1\" in -s) printf '%s\\n' \"$QWEN_TEST_OS\";; -m) printf '%s\\n' \"$QWEN_TEST_ARCH\";; esac\n")
			write(filepath.Join(shim, "sysctl"), "#!/bin/sh\nprintf '%s\\n' \"$QWEN_TEST_HARDWARE_ARM\"\n")
			write(filepath.Join(shim, "sudo"), "#!/bin/sh\necho 'unexpected sudo' >&2\nexit 99\n")
			write(filepath.Join(shim, "curl"), `#!/bin/sh
set -eu
output=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) output=$2; shift 2 ;;
        -w|--retry|--connect-timeout|--proto|--proto-redir) shift 2 ;;
        https://*) url=$1; shift ;;
        *) shift ;;
    esac
done
printf '%s\n' "$url" >> "$QWEN_TEST_FIXTURE/requests"
case "$url" in
    https://github.com/mitchwolfe1/qwen-cli/releases/latest)
        printf 'https://github.com/mitchwolfe1/qwen-cli/releases/tag/v1.2.3'
        ;;
    https://github.com/mitchwolfe1/qwen-cli/releases/download/v1.2.3/*)
        cp "$QWEN_TEST_FIXTURE/${url##*/}" "$output"
        ;;
    *) echo "unexpected URL: $url" >&2; exit 90 ;;
esac
`)
			path := shim + string(os.PathListSeparator) + os.Getenv("PATH")
			if test.shasumOnly {
				for _, name := range []string{"shasum", "awk", "mktemp", "mkdir", "install", "mv", "rm", "cp"} {
					program, err := exec.LookPath(name)
					if err != nil {
						t.Skipf("%s is unavailable", name)
					}
					if err := os.Symlink(program, filepath.Join(shim, name)); err != nil {
						t.Fatal(err)
					}
				}
				path = shim
			}
			cmd := exec.Command("/bin/sh", "install.sh")
			cmd.Env = []string{
				"PATH=" + path,
				"QWEN_INSTALL_DIR=" + target,
				"QWEN_TEST_OS=" + test.system,
				"QWEN_TEST_ARCH=" + test.arch,
				"QWEN_TEST_HARDWARE_ARM=" + test.hardwareARM,
				"QWEN_TEST_FIXTURE=" + fixture,
			}
			output, err := cmd.CombinedOutput()
			installed, readErr := os.ReadFile(filepath.Join(target, "qwen"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if test.badChecksum || test.unsupported {
				if err == nil || string(installed) != "previous binary" {
					t.Fatalf("invalid download/platform changed installation: %v, %q, %s", err, installed, output)
				}
				if test.badChecksum && !strings.Contains(string(output), "checksum mismatch") {
					t.Fatalf("missing checksum error: %s", output)
				}
				return
			}
			if err != nil || string(installed) != "fixture for "+test.asset {
				t.Fatalf("install failed: %v, %q, %s", err, installed, output)
			}
			info, err := os.Stat(filepath.Join(target, "qwen"))
			if err != nil || info.Mode().Perm() != 0755 {
				t.Fatalf("binary is not executable: %v, %v", info, err)
			}
			requests, err := os.ReadFile(filepath.Join(fixture, "requests"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(requests), "/releases/latest") != 1 ||
				!strings.Contains(string(requests), "/releases/download/v1.2.3/"+test.asset) ||
				!strings.Contains(string(requests), "/releases/download/v1.2.3/SHA256SUMS") {
				t.Fatalf("installer did not pin both downloads to the same personal release: %s", requests)
			}
		})
	}
}
