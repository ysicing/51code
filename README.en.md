<p align="center">
  <img src="resources/icon.png" width="128" height="128" alt="51Code app icon">
</p>

# 51Code

[简体中文](README.md) | English

51Code is a lightweight, open-source Git desktop client focused on the everyday workflows people use in SourceTree. It runs on macOS, Windows 11 x64 and Linux x64/arm64 (deb and rpm packages, mainly tested on Ubuntu 24.04 LTS). The interface is built with Go and [MyGo Native UI](https://mygo.egoist.dev/docs/ui), and it uses the system Git.

## Features

- **Repositories**: add and scan local repositories, with groups and search
- **Changes and commits**: file filters, bulk staging, hunk/line staging, commit drafts, adaptive unified/side-by-side diff, read-only full file view with in-file search
- **History**: commit graph, full-text search, path filter, commit diffs and comparison between any two revisions
- **Branches and tags**: manage local branches and tags, check out remote branches, first publish and upstream tracking
- **Remotes**: manage remote URLs, Fetch, fast-forward Pull, Push, and ahead/behind counts
- **Stashes and merges**: manage stashes, merge branches, edit conflicts, and continue or abort in-progress Git operations
- **Worktrees**: discover, open and create worktrees, merge into the current worktree, and remove merged worktrees after checking their state
- **Desktop experience**: light/dark appearance, native menus, keyboard shortcuts, resizable panes and signed in-app updates; sidebar entries for search, comparison, stashes and worktrees can be shown or hidden individually

51Code currently manages repositories that already exist on your machine. There is no clone entry, interactive rebase or credential settings yet; remote operations use your system Git/SSH credentials. A recovery copy is saved before discarding file changes. See the [usage guide](docs/usage.md) (Chinese).

## Installation

macOS:

```sh
brew install --cask ysicing/tap/51code
```

Windows and Linux:

- Windows: download `51code-setup-<version>-amd64.exe` from [GitHub Releases](https://github.com/ysicing/51code/releases) and install it; it updates itself afterwards. Install [Git for Windows](https://gitforwindows.org/) first and make sure `git` is on PATH. The installer is not Authenticode-signed.
- Linux (recommended, with automatic updates): `curl -fsSL https://c.ysicing.net/oss/apps/macOS/51code/install.sh | sh`, which installs into `~/.local`.
- Linux packages: download `51code_<version>_<amd64|arm64>.deb` (`sudo apt install ./<file>`) or `51code-<version>-1.<x86_64|aarch64>.rpm` (`sudo dnf install ./<file>`) from GitHub Releases. Packages install into `/opt` and cannot update themselves; use "Settings → Updates → Download New Version…" to get a new package and upgrade manually.

Windows/Linux builds of v0.2.0 and earlier need one manual upgrade; after that they update as described above.

Linux requires a GTK 3 desktop. Saving AI keys also requires a running Secret Service (such as GNOME Keyring). On minimal desktops without one, 51Code shows a save error and keeps your draft instead of falling back to plain-text storage.

## Running locally

Requires Go **1.27.3+**, Bun, Git and Task; macOS also needs the Xcode Command Line Tools, and Ubuntu needs GTK 3. On Windows, start it with `bun run dev`. Installers for all three platforms are cross-built on macOS.

```sh
task setup
task dev
```

Build the app and installer for the current platform:

```sh
task build
```

macOS output goes to `build/darwin-arm64/` or `build/darwin-amd64/`. Local builds use ad-hoc signing; the release process uses Developer ID signing, Apple notarization and Gatekeeper checks. Windows and Linux installers are built and published together by `task release` on macOS; see [Development and release](docs/development.md) (Chinese).

Run `task` to list all tasks. `task fmt` adds license headers and formats Go code. `task check` runs header and format checks, Go tests, native UI tests, release script tests and golangci-lint in sequence. Bun is only used for packaging tools and release scripts; the UI needs no WebView or frontend build.

## Documentation

- [Usage guide](docs/usage.md) (Chinese): shortcuts, updates, recovery copies and current limits
- [Development and release](docs/development.md) (Chinese): native UI development, testing, project layout and object storage release flow
- [Changelog](CHANGELOG.md)

## License

Copyright (c) 2026 ysicing

This project is licensed under the [GNU AGPL v3.0](LICENSE) (`AGPL-3.0-only`).
