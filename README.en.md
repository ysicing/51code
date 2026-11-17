<p align="center">
  <img src="resources/icon.png" width="96" height="96" alt="51Code app icon">
</p>

<h1 align="center">51Code</h1>

<p align="center">A lightweight, open-source desktop client for everyday Git workflows.</p>

<p align="center"><a href="README.md">简体中文</a> · English</p>

<p align="center">
  <a href="https://github.com/ysicing/51code/releases/latest">Download</a> ·
  <a href="docs/usage.md">Usage guide</a> ·
  <a href="CHANGELOG.md">Changelog</a>
</p>

## Installation

| Platform | Supported systems | Install |
| --- | --- | --- |
| macOS | Apple Silicon / Intel | [Download DMG](https://github.com/ysicing/51code/releases/latest) or Homebrew |
| Windows | Windows 11 x64 | [Download installer](https://github.com/ysicing/51code/releases/latest), with Git, SSH and Git LFS included |
| Linux | x64 / arm64 | Install script, APT / YUM repositories, or [deb / rpm packages](https://github.com/ysicing/51code/releases/latest) |

<details>
<summary>macOS · Homebrew</summary>

```sh
brew install --cask ysicing/tap/51code
```

Releases are Developer ID-signed and Apple-notarized, with in-app updates.

</details>

<details>
<summary>Windows · Installer</summary>

Choose `51code-setup-<version>-amd64.exe` from the download page. In-app updates are supported; no separate Git installation is required. The installer is not yet Authenticode-signed.

</details>

<details>
<summary>Linux · Install script (with in-app updates)</summary>

```sh
curl -fsSL https://c.ysicing.net/oss/apps/macOS/51code/install.sh | sh
```

Installs into `~/.local` with in-app updates.

</details>

<details>
<summary>Debian / Ubuntu · APT</summary>

```sh
echo "deb [trusted=yes] https://mirrors.china.12306.work/repository/ysicing/apt/ /" | sudo tee /etc/apt/sources.list.d/ysicing.list
sudo apt-get update
sudo apt-get install 51code
```

Use APT for future upgrades. To install a downloaded deb package manually, use `sudo apt install ./<file>.deb`.

</details>

<details>
<summary>YUM-based distributions · YUM</summary>

Write the following configuration to `/etc/yum.repos.d/fury.repo`:

```ini
[fury]
name=Gemfury Private Repo
baseurl=https://yum.fury.io/ysicing/
enabled=1
gpgcheck=0
```

Then install, and use YUM for future upgrades:

```sh
sudo yum install 51code
```

To install a downloaded rpm package manually, use `sudo dnf install ./<file>.rpm`.

</details>

Linux requires a GTK 3 desktop and system Git, and is mainly tested on Ubuntu 24.04 LTS. Saving AI keys also requires a Secret Service such as GNOME Keyring. deb / rpm packages install into `/opt`; upgrade through the package manager or a new package, as in-app automatic updates are unavailable. Windows / Linux versions v0.2.0 and earlier need one manual upgrade first.

## Everyday workflows

Inspired by the everyday operations in SourceTree, with a native interface built using Go and [MyGo Native UI](https://mygo.egoist.dev/docs/ui), without a WebView.

| Workflow | Capabilities |
| --- | --- |
| Organize repositories | Separate repository browser, multiple repository windows, add and scan, groups, search and primary language detection |
| Review and commit | File filters, bulk and hunk / line staging, commit drafts and AI commit messages |
| Read diffs | Adaptive unified / side-by-side diff, syntax highlighting, read-only full files and in-file search |
| Explore history | Commit graph, full-text search, path filters, commit diffs and comparison between any two revisions |
| Branch and sync | Branches and tags, remote branch checkout, first publish and upstream tracking, remote URLs, Fetch / fast-forward Pull / Push and ahead / behind counts |
| Stash and merge | Stash management, branch merges, conflict editing, and continuing or aborting Git operations |
| Use worktrees | Discover, open and create worktrees; merge and remove merged worktrees after checking their state |
| LFS and insights | Git LFS, large-file checks, commit activity, contributors and file hotspots |

Light / dark appearance, native menus, keyboard shortcuts and resizable panes. Sidebar entries such as search, comparison, stashes and worktrees can be shown or hidden individually.

## Getting started

1. Add an existing local repository, or scan a folder to add several.
2. Review diffs, stage your changes, and write or generate a commit message with AI.
3. Commit, then fetch, pull or push from the toolbar.

Cloning, interactive rebase and authentication settings are not yet available in the UI. Remote operations use the selected Git installation's credentials. A recovery copy is saved before discarding file changes. AI sends staged diffs only after confirmation and never commits or pushes automatically. See the [usage guide](docs/usage.md) (Chinese).

## Documentation and development

- [Usage guide](docs/usage.md) (Chinese): workflows, shortcuts, AI setup, updates and recovery copies
- [Development and release](docs/development.md) (Chinese): setup, native UI development, testing, builds and releases
- [Changelog](CHANGELOG.md): version history

Local development requires Go **1.27.3+**, Bun, Git and Task; macOS also needs Xcode Command Line Tools.

```sh
task setup
task dev
```

`task build` builds the local app. `task check` runs format checks, tests and lint in sequence. See the development guide for platform requirements and all commands.

## License

Copyright (c) 2026 ysicing · [GNU AGPL v3.0](LICENSE) (`AGPL-3.0-only`)
