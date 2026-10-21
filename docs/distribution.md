# Distribution and releases

Workbox is distributed through GitHub Releases and the self-managed `MaybeLL/homebrew-tap`. The implementation is Go with a pure-Go SQLite driver. Precompiled binaries need Git but no Go, Python or separately installed SQLite.

## GitHub Releases

Publish `workbox_VERSION_OS_ARCH.tar.gz` and `workbox_VERSION_checksums.txt` on the project's Releases page. Targets are `darwin` and `linux`, each with `arm64` and `amd64`. Each archive contains the executable, project license, bundled dependency notices, README and guides. Select the matching target, verify its SHA-256, extract it and put `workbox` in PATH.

For example, after downloading the matching archive and checksum file (replace VERSION):

```sh
# Inspect the expected hash in workbox_VERSION_checksums.txt and compare:
shasum -a 256 workbox_VERSION_darwin_arm64.tar.gz
tar -xzf workbox_VERSION_darwin_arm64.tar.gz
mkdir -p ~/.local/bin
install -m 755 workbox ~/.local/bin/workbox
workbox --version
```

Ensure `~/.local/bin` is in PATH. Linux can use `sha256sum`. Release binaries are not currently signed/notarized; follow your system's approval flow for downloaded executables when required.

## Homebrew

The `MaybeLL/homebrew-tap` repository contains `Formula/workbox.rb`. Install with:

```sh
brew install MaybeLL/tap/workbox
```

Upgrade with:

```sh
brew update
brew upgrade MaybeLL/tap/workbox
```

The formula pins a release tag and full Git revision. The release workflow builds native macOS bottles on macOS 15 for ARM64 and Intel, merges their verified checksums into the formula, then publishes the complete release. Matching systems install the bottle; other systems build from the pinned source with Homebrew-managed Go. Git uses the macOS-provided tool where available and is otherwise a dependency. No application to Homebrew's official repositories is required.

Publish the final `workbox.rb` release asset as `Formula/workbox.rb` in the tap. Use the final merged formula, not the pre-bottle definition from a local packaging run. These are standard Homebrew bottles; the installer does not remove quarantine attributes or change system security settings. Direct release archives remain unsigned/not notarized, so their first-run behavior depends on the download method and the user's normal macOS approval flow.

## Prepare a release

Use Go 1.26+ in module mode:

```sh
go test ./...
go test -race ./...
make release VERSION=0.1.0
```

The new `dist` directory contains four CGO-free binaries in archives, checksums, and the initial Homebrew source formula. The output directory must not already exist. Packaging uses the Go standard library and Go tooling; failures preserve partial artifacts. Source paths are removed using `-trimpath`.

Validate the native executable using `sh scripts/check-install.sh /path/to/workbox`, verify archive hashes and formula syntax, then test Homebrew installation using the exact public artifacts. The script isolates configuration and workspaces in a temporary directory. Test upgrade from the previous release once one exists. Cross-compilation alone does not prove runtime compatibility; CI runs tests on macOS and Linux.

CI tests both systems. The release workflow runs on version tags such as `v0.1.0`, tests, packages a draft, builds and tests bottles, merges their metadata, then publishes all assets together. Public archive installation checks run after publication. Updating the separate tap is an explicit maintainer step; the workflow requires no cross-repository token.

Publish the tap formula after its referenced release archives and bottles are available. Never replace an existing published release artifact under the same version; use a new patch version for a correction.
