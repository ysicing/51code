<p align="center">
  <img src="resources/icon.png" width="96" height="96" alt="51Code 应用图标">
</p>

<h1 align="center">51Code</h1>

<p align="center">轻量、开源的跨平台 Git 桌面客户端，专注日常 Git 工作流。</p>

<p align="center">
  <a href="https://awesomemygo.dev/apps/51code"><img src="https://awesomemygo.dev/badge.svg" alt="已收录于 Awesome MyGo"></a>
</p>

<p align="center">简体中文 · <a href="README.en.md">English</a></p>

<p align="center">
  <a href="https://github.com/ysicing/51code/releases/latest">下载最新版</a> ·
  <a href="docs/usage.md">使用指南</a> ·
  <a href="CHANGELOG.md">更新日志</a>
</p>

## 安装

| 平台 | 支持范围 | 安装方式 |
| --- | --- | --- |
| macOS | macOS 12+，Apple Silicon / Intel | [下载 DMG](https://github.com/ysicing/51code/releases/latest) 或 Homebrew |
| Windows | Windows 11 x64 | [下载安装器或免安装 ZIP](https://github.com/ysicing/51code/releases/latest)，均内置 Git、SSH 与 Git LFS |
| Linux | x64 / arm64 | 安装脚本、APT / YUM 源，或 [deb / rpm 软件包](https://github.com/ysicing/51code/releases/latest) |

<details>
<summary>macOS · Homebrew</summary>

```sh
brew install --cask ysicing/tap/51code
```

正式版本经过 Developer ID 签名与 Apple 公证，支持应用内更新。

</details>

<details>
<summary>Windows · 安装器与免安装版</summary>

从下载页选择 `51code-setup-<版本>-amd64.exe`，安装后支持应用内更新，无需另行安装 Git。安装器暂未提供 Authenticode 签名。

免安装版下载 `51code-<版本>-windows-amd64-portable.zip`，完整解压到一个目录后运行 `51Code.exe`。应用设置保存在当前用户的数据目录。

</details>

<details>
<summary>Linux · 安装脚本（支持应用内更新）</summary>

```sh
curl -fsSL https://c.ysicing.net/oss/apps/macOS/51code/install.sh | sh
```

安装到 `~/.local`，支持应用内更新。

</details>

<details>
<summary>Debian / Ubuntu · APT</summary>

```sh
echo "deb [trusted=yes] https://mirrors.china.12306.work/repository/ysicing/apt/ /" | sudo tee /etc/apt/sources.list.d/ysicing.list
sudo apt-get update
sudo apt-get install 51code
```

后续通过 APT 升级。手动下载 deb 包时，使用 `sudo apt install ./<文件>.deb`。

</details>

<details>
<summary>YUM 系发行版 · YUM</summary>

将以下配置写入 `/etc/yum.repos.d/fury.repo`：

```ini
[fury]
name=Gemfury
baseurl=https://yum.fury.io/ysicing/
enabled=1
gpgcheck=0
```

然后安装，后续通过 YUM 升级：

```sh
sudo yum install 51code
```

手动下载 rpm 包时，使用 `sudo dnf install ./<文件>.rpm`。

</details>

Linux 需要 GTK 3 桌面环境和系统 Git，主要在 Ubuntu 24.04 LTS 上验证；保存 AI 密钥还需 Secret Service（如 GNOME Keyring）。deb / rpm 安装到 `/opt`，通过包管理器或下载新包升级，不支持应用内自动更新。v0.2.0 及更早的 Windows / Linux 版本需先手动升级一次。

## 日常工作流

以 SourceTree 常用操作为参考，使用 Go 与 [MyGo Native UI](https://mygo.egoist.dev/docs/ui) 构建原生界面，无需 WebView。

| 工作流 | 能力 |
| --- | --- |
| 管理仓库 | 多仓库窗口、添加、扫描、克隆与初始化、默认仓库组、搜索、最近打开排序、仓库重新定位、主要编程语言识别 |
| 检查与提交 | 文件筛选、批量与逐块 / 逐行暂存、暂存区密钥扫描、提交草稿、AI 提交说明 |
| 阅读代码 | 自适应统一 / 并排 Diff、文件浏览与图片预览、快速打开、语法高亮、只读全文、行上下文跳转与文件内查找 |
| 审阅变更 | 行级意见与 Markdown 导出、已审阅标记、生成文件折叠、图片前后对照、合并提交父版本切换 |
| 追溯历史 | 提交关系图、全文搜索、路径筛选、提交差异与任意两版本比较 |
| 分支与同步 | 分支与标签、远程分支检出、首次发布与上游跟踪、远程地址、Fetch / 快进 Pull / Push、领先 / 落后计数 |
| 贮藏与合并 | 贮藏管理、分支合并、冲突编辑、继续或中止 Git 操作 |
| 多工作树 | 发现、打开、创建与合并工作树，筛选可清理目录，复核后移除附加工作树并保留分支 |
| LFS 与洞察 | Git LFS、大文件检查、提交活跃度、贡献者与文件热点 |

浅色 / 深色外观、原生菜单、快捷键和可调整面板；搜索、版本比较、贮藏和工作树等侧栏入口可独立显示或隐藏。

## 开始使用

1. 在仓库列表添加已有仓库、扫描文件夹、克隆远程仓库，或将本地目录初始化为 Git 仓库。
2. 查看文件差异，暂存需要提交的内容，填写或使用 AI 生成提交说明。
3. 提交后通过工具栏获取、拉取或推送。

远程操作沿用所选 Git 的凭证配置。交互式 rebase 通过命令行发起。开启回收站后，丢弃文件修改前会保存恢复副本。AI 提交说明生成需先授权发送暂存差异，提交和推送由你操作。详见[使用指南](docs/usage.md)。

## 文档与开发

- [使用指南](docs/usage.md)：功能操作、快捷键、AI 配置、更新与恢复副本
- [开发与发布](docs/development.md)：环境准备、原生界面开发、测试、构建与发布
- [更新日志](CHANGELOG.md)：版本变化

本地开发需要 Go **1.27.3+**、Bun、Git 和 Task；macOS 另需 Xcode Command Line Tools。

```sh
task setup
task dev
```

`task build` 构建本地应用，`task check` 串行运行格式检查、测试与 lint。各平台要求及完整命令见开发文档。

## License

Copyright (c) 2026 ysicing · [GNU AGPL v3.0](LICENSE)（`AGPL-3.0-only`）
