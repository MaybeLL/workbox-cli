# Distribution and releases

Confirmed on 2026-10-21: workbox uses GitHub Releases and a self-managed Homebrew tap. The implementation is Go with a pure-Go SQLite driver. Precompiled binaries need Git but no Go, Python or separately installed SQLite. Neither public channel has been published yet.

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

Maintain the installation definition in the planned `MaybeLL/homebrew-tap` repository. After the tap and a downloadable release are published and tested, the intended installation command is:

```sh
brew install MaybeLL/tap/workbox
```

The intended upgrade flow is:

```sh
brew update
brew upgrade MaybeLL/tap/workbox
```

The generated definition selects precompiled archives by system and architecture, pins real hashes, and declares Git as a dependency. Copy `dist/homebrew/workbox.rb` to `Formula/workbox.rb` in the tap for each release, test, then publish the tap update. No application to Homebrew's official repositories is required.

## Prepare a release

Use Go 1.26+ in module mode:

```sh
go test ./...
go test -race ./...
make release VERSION=0.1.0
```

The new `dist` directory contains four CGO-free binaries in archives, checksums, and the generated Homebrew definition. The output directory must not already exist. Packaging uses the Go standard library and Go tooling; failures preserve partial artifacts. Source paths are removed using `-trimpath`.

Validate the native executable in an isolated empty-workspace flow, verify archive hashes and Homebrew definition syntax, then test Homebrew installation using the exact artifacts. Test upgrade from the previous release once one exists. Cross-compilation alone does not prove runtime compatibility; CI runs tests on macOS and Linux.

CI tests both systems. The release workflow runs on version tags such as `v0.1.0`, tests, packages and publishes GitHub release assets, including the generated tap definition. Updating the separate tap is an explicit maintainer step; the workflow requires no cross-repository token.

## Current state

The public project repository exists. The implementation is local and staged for review. No public application release or Homebrew tap has been published, so the commands above describe the planned distribution experience.

Source commits, tags, releases and tap updates follow existing publication authorization. The current checkout remains local for review; no publication has been triggered.
