# 开发与发布

[返回首页](../README.md) · [使用说明](usage.md)

## 本地开发

需要 Go **1.27.3+**、Bun、Git 和 Task；macOS 需要 Xcode Command Line Tools。所有任务定义在 [Taskfile.yml](../Taskfile.yml)，运行 `task` 查看列表。

```sh
task setup
task dev
```

界面直接使用 MyGo 的 `ui` 和 `ui/editor` Go 组件，不需要 TypeScript 绑定、Vite 服务或前端资源构建。Bun 仅用于 MyGo 打包工具与发布脚本。Git 请求在后台执行，通过 `Window.Update` 将结果交回界面线程。

界面翻译使用 MyGo 原生 i18n，消息目录位于 `locales/`，源语言为 `zh-Hans`，同时维护 `zh-Hant` 和 `en`。修改消息后运行 `bun run mygo generate` 更新并提交 `locales/catalog_gen.go`，通过 `bun run mygo i18n check` 检查翻译完整性。语言选择由 MyGo 持久化；界面标签使用翻译，已有配置值和 Git 引用保持不变。

文件差异、提交详情、贮藏预览和历史搜索使用局部加载状态，不禁用整个工作区；连续选择会取消旧读取，仅允许当前仓库、页面及最后一次请求回填结果。导航、写操作和窗口关闭会取消未完成的读取；写操作等待已取消的读取退出后执行。重复点击已加载的文件保留 Diff 编辑器状态。

`nativeDesktop` 在界面线程管理一个仓库列表窗口和按规范工作区路径索引的仓库窗口。同一仓库从子目录或符号链接打开时复用窗口，不同 Worktree 使用各自的根路径。`nativeApp` 的选择、阅读、对话框、上下文及任务取消器属于窗口；`RepositoryService` 每窗口独立，只共享线程安全的 workspace manager。应用偏好共用一个实例，通过原有原子写入保存，避免多份快照覆盖其他仓库草稿；应用设置面板只允许一份，外观和后台行为修改同步到其他窗口。提交图维护的尝试记录与串行槽仍由应用共享。

菜单命令按 MyGo 提供的活动窗口分发，原生菜单跟踪期间缺少焦点窗口时沿用最后激活的应用窗口；仓库列表、添加和分组命令定位列表窗口。窗口使用独立的 `StateKey` 保存几何状态。退出前在 `OnBeforeQuit` 中预检所有窗口，全部通过后才允许逐个关闭，取消退出时清除预检标记；预检期间阻止重入关闭及新任务，避免后续窗口的拒绝造成部分窗口先被关闭。系统强制结束会话时只尝试保存，不弹出无法取消的确认。

窗口聚焦通过独立的后台检查更新当前仓库，不复用全局忙碌的手动刷新。引用未变化时不查询历史；引用变化时按原请求重读已加载范围，并沿用列表身份及滚动锚点。不能仅比较文件的 M/A 状态来判断内容变化，当前工作区预览还需校验实际 Diff 与补丁。检查与前台读取共用 `readDone` 完成链；用户操作优先取消检查，写入必须等读取退出，过期结果不得回填。后台 fetch 进行中时，聚焦检查先加入刷新队列，等 fetch 和期间启动的前台任务退出后再执行。重读已加载历史时一次读取（超过 `gitrepo.MaxHistoryReloadSize` 才分块），不按页 `--skip` 重复遍历。

各仓库窗口的原生文件监听通过 MyGo `WatchFiles` 驱动，工作区使用 `HintsOnly`，Git 目录仅监听索引、引用、配置和操作状态；链接工作树同时监听独立 Git 目录与共享目录。事件合并后复用聚焦刷新，不另开状态轮询；前台任务、弹窗和未保存的冲突编辑优先，窗口关闭时释放所属监听与计时器，显示列表不会停止其他仓库窗口的监听。连续刷新结果无变化时，排队延迟从 500ms 逐次加倍至 8s，结果有变化、窗口聚焦或用户操作结束后复位，避免被忽略的构建目录持续写入时不停读取。每次已应用的完整读取（写操作后的读取、聚焦或监听刷新）记录开始时间，排队的变化若都在该时间之前观察到则跳过刷新；监听批次不带变化时间，只能以收到批次的时刻为界，写操作自身产生的尾随批次仍会触发一次无变化的刷新。排队、延后和启动 fetch 等纯调度经 `mygo.RunOnMain` 执行，不触发整窗重绘。监听出错后按 1s／5s／30s 重建订阅并补查，仍失败或达到监听上限才提示。可选的后台 fetch 同样注册到 `readDone`，取消后必须等 Git 进程退出才让前台继续，禁用 fetch 附带的自动维护；失败不改变仓库可写状态。设置写入成功后才重启后台行为，失败回滚界面值。

退出保护区分可取消的读取与写入：仓库列表刷新、仓库状态刷新和目录扫描不阻止关闭，窗口关闭后统一取消上下文并忽略迟到结果。提交、推送等写操作完成前仍阻止退出，并提示正在执行的任务；不能仅凭界面的忙碌状态阻止退出。

AI 协议适配位于 `internal/commitai/`。空协议保留原有 Magpie 自动发现和手动本机地址行为；自定义服务支持 OpenAI Responses、Chat Completions 和 Claude Messages。自定义服务不继承 `MAGPIE_API_KEY`，不跟随重定向，远程端点只允许 HTTPS。显式填写模型 ID 时不依赖模型列表接口；暂存内容发送范围及生成后的差异校验保持一致。

`internal/aikeychain/` 使用已有 purego 依赖调用 macOS Security.framework，不通过命令行传递密钥。钥匙串服务名为 `com.ysicing.xsourcetree.ai`，账号由协议与规范化 Base URL 的 SHA-256 生成。密钥不参与配置 JSON 序列化；读写在后台完成，失败不降级为明文保存或忽略凭证错误发送请求。设置保留未保存的密钥草稿，保存失败允许重试，关闭前确认放弃，写入期间阻止关闭。

Windows 和 Ubuntu 的同一凭证接口使用 MyGo `Secrets.Service`，分别落到 Windows Credential Manager 和 Linux Secret Service。macOS 保留现有实现、服务名及账号生成规则。Ubuntu 除 GTK 3 外，AI 密钥保存需要 libsecret、D-Bus 会话及 Secret Service；服务缺失、锁定或拒绝访问时保留错误和未保存草稿。不会将密钥写入配置文件。`XSOURCETREE_TEST_KEYCHAIN=1` 的合成凭证读写用例支持三平台，默认关闭。

MyGo `Secrets.Service` 在 macOS 同样使用系统钥匙串，指定相同服务名与账号即可复用旧记录。当前保留 macOS 实现以维持原有 64 KiB 密钥上限；MyGo 的跨平台写入上限为 2560 字节。macOS 的可选凭证测试覆盖旧实现与 MyGo 的双向读写及删除互通，仅操作独立服务下的合成记录。

### 改名兼容

品牌展示名为 `51Code`，Go 模块为 `github.com/ysicing/51code`，安装包前缀使用小写 `51code`。Bundle ID 继续使用 `com.ysicing.xsourcetree`，钥匙串服务名及账号生成规则保持不变，以复用旧 API Key 和系统偏好。

MyGo 根据应用名选择数据目录和单实例锁。启动时先用旧名称 `XSourceTree`（开发版为 `XSourceTree Dev`）固定数据目录并取得原锁，再恢复新显示名。macOS 继续读取 `~/Library/Application Support/XSourceTree`，包括仓库列表、草稿、偏好与恢复副本；不复制、移动或覆盖旧数据。自动化提前设置的数据目录仍优先使用。旧目录不可用时停止启动，不静默启用空配置。

已有 `XSOURCETREE_*` 构建和测试环境变量、`XSourceTreeCommit`／`XSourceTreeBuildTime` 元数据字段及仓库外更新私钥路径继续兼容。图标内容未变，历史图标源文件名保留。

### gops 运行时诊断

gops agent 默认关闭，仅在启动时设置 `XSOURCETREE_GOPS=1` 才会在取得单实例锁后启动，开发版和正式版均支持。诊断端口无鉴权，本机任意进程都能修改 GC、触发采样或导出调用栈，排查结束后应去掉该变量重启应用。只监听 `127.0.0.1` 的随机端口，使用 gops 默认目录中的 PID 文件供命令行发现；可通过 `GOPS_CONFIG_DIR` 指定目录，应用和命令行须使用相同值。初始化失败仅记录日志，不阻止应用启动。

```sh
go install github.com/google/gops@v0.3.29
gops
gops stats <PID>
gops memstats <PID>
gops stack <PID>
gops pprof-heap <PID>
gops pprof-cpu <PID>
```

从 `gops` 列表选择 `51Code` 或 `51Code Dev` 的 PID；带 `*` 表示 agent 已启用。这些命令面向 Go 运行时，不包含 Git、SSH、Git LFS 子进程或原生界面分配的全部内存。CPU profile 按 gops 协议采集约 30 秒，堆采样沿用 Go 默认采样率。诊断端口允许本机其他进程访问，采样文件可能包含调用栈及路径，应按需保管。

agent 不接管退出信号，清理由 MyGo 的最终退出钩子同步执行；取消退出时仍可诊断。正常退出和启动失败会关闭监听并移除 PID 文件，强制终止（如 `SIGKILL`）不保证清理。

### 本地构建

构建 macOS 应用与 DMG：

```sh
task build
```

产物位于 `build/darwin-arm64/`（Intel 机器为 `darwin-amd64`）。本地构建默认使用 ad-hoc 签名；正式发布由发布脚本启用 Developer ID 签名、Hardened Runtime、Apple 公证和票据装订。应用更新包另用 Ed25519 签名校验，不能替代 macOS 的代码签名与公证。

`task build`、`bun run build` 和正式发布统一经过 `scripts/build.mjs`。每次成功构建，构建号递增 1，同次构建的全部架构和平台共用一个号；失败不会消耗构建号。计数及最近一次成功构建的信息保存在当前工作区的 `.mygo/build-info.json`，不提交 Git；删除该文件或换用新工作区会从 1 开始，正式发布应沿用发布工作区的记录。

正式版本仍由 `package.json` 的 `version` 控制，不随本地构建自动修改。右下角状态栏显示例如 `v0.0.1 (12)`，点击打开“关于”窗口；悬停和“关于”窗口可查看构建号、Git short commit 和构建时间。未提交修改的构建附加 `-dirty`。时间以 UTC 写入构建，界面按本地时区显示；macOS 安装包同时写入 `CFBundleVersion`、`XSourceTreeCommit` 和 `XSourceTreeBuildTime`。更新清单、下载文件名和版本 tag 继续使用正式版本号。`task dev` 和直接 `go build` 不递增打包构建号。

本地构建产物与已安装应用共用单实例锁，新旧品牌也共用原锁。再次启动时，新进程退出并激活已有窗口，因此已有应用运行时，打开新产物不会切换到新代码。预览新构建前，应先通过应用菜单正常退出已有版本，再直接打开构建目录中的 `51Code.app`，无需覆盖已安装应用。代理不得擅自退出用户启动的进程；需要切换时先说明并取得授权。普通预览不得修改 `XDG_RUNTIME_DIR`、`TMPDIR` 或应用标识来绕过单实例锁；独立数据的 `e2e` 测试仍按下文流程执行。

应用图标源文件为 `resources/icon.png`，MyGo 构建时自动使用。透明底 Logo 位于 `assets/brand/logo.png`，macOS 图标文件位于 `assets/brand/XSourceTree.icns`。

## 验证

`task fmt`（或 `task format`）参考 Gitness，先补齐文件头，再通过 `golangci-lint fmt` 统一执行 gofmt、goimports 和 gci。规则集中在 [.golangci.yml](../.golangci.yml)，导入分组为标准库、项目包、第三方包、匿名导入、点导入。覆盖新建但尚未暂存的 Go 文件，跳过依赖、构建目录和生成文件。

golangci-lint、addlicense 和 govulncheck 优先复用 `PATH` 中的工具，缺少时使用 `go install ...@latest` 安装，不指定项目专用安装目录。Go 工具安装目录应包含在 `PATH` 中。任务串行执行，Go 并发限制为 2，工具编译使用 `-p=1`，addlicense 每次只处理一个文件。`task fmt:check` 检查文件头是否缺失及格式差异，不修改源码；`task lint` 使用 goheader 校验 Go 版权头内容，并运行格式检查及 govet。

```sh
task fmt
task fmt:check
task lint
```

`task license`（或 `task gencopyright`）单独补齐版权头，`task license:check` 只检查是否缺失。覆盖项目的 Go、TypeScript、MJS、CSS、HTML 和 YAML 文件，包括测试和平台专用源码；生成文件、代理技能目录、依赖及构建产物跳过。JSON、锁文件、文档和图片不添加代码注释。

文件头使用以下内容，新增文件采用当前年份，已有年份和版权声明不会被批量覆盖。goheader 接受年份或年份区间，避免跨年后要求重写所有文件：

```go
// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only
```

addlicense 检查所有适用文件的头部是否存在；golangci-lint 的 goheader 严格检查当前平台参与构建的 Go 文件，包含测试及 `e2e` 文件。已有版权头与项目模板不一致时需人工确认来源，不覆盖第三方声明。

`task gomod` 单独运行 `go mod tidy`，不随格式化修改依赖。`task check` 串行执行文件头与格式检查、Go 测试（包含原生界面）、发布脚本测试和 golangci-lint。所有 Git 集成测试只操作测试创建的临时仓库，包括本地 bare 远程仓库，不连接真实远程服务。

```sh
task check
```

可用 `task test:go`、`task test:ui`、`task test:release` 或 `task vet` 单独执行检查，`task test` 依次执行 Go 和发布脚本测试。Go 测试使用 `GOMAXPROCS=2`、`-p=1` 和 `-parallel=2`；开发与构建任务同样限制 Go 构建并发。

`task govulncheck` 单独扫描当前平台及 Go 工具链下可调用代码的已知漏洞，需要访问 Go 漏洞数据库，不包含在默认 `task check` 中。

LFS 用例默认使用隔离的工具替身，不安装工具或修改用户 Git 配置。设置 `XSOURCETREE_TEST_LFS=/绝对路径/git-lfs` 后，`GOMAXPROCS=2 go test -p=1 -parallel=2 ./internal/gitrepo -run TestRealLFS -count=1` 额外验证真实指针转换、属性草稿与部分暂存保护，以及本地 bare 远程的 LFS 上传和缺失内容下载；不访问外部远程。所有新跟踪规则按具体文件名匹配，转换在持有真实索引锁时从最新索引合入指定条目，再原子发布，保留无关暂存内容；取消时只清理本次持有的锁。属性回滚不截断原文件，原 inode 保留在 Git 索引目录下的 `.51code-lfs-attributes-*` 恢复目录，避免丢失编辑器并发写入。全局／仓库开关保存在客户端偏好中，不能靠关闭客户端集成来卸载仓库已有过滤器或 hooks。

加载基准覆盖 100／5000 个变更文件的原生列表重绘、单文件历史／比较读取，以及 1 万提交的首屏、后页和搜索。使用 `GOMAXPROCS=2 go test -p=1 -parallel=2 ./internal/app ./internal/gitrepo -run '^$' -bench 'Benchmark(NativeChangedFilesFrame|RevisionFileLoading|HistoryLoading)$' -benchtime=3x -count=1` 串行复测；基准仓库由测试临时生成，不访问真实远程。比较相同场景的耗时与分配，避免把冷启动、后台负载或不同数据规模混为优化收益。

原生界面测试使用 MyGo `ui.Tester`，覆盖侧栏切换与键盘导航、文件排序菜单、中文文本输入、统一/并排 Diff、提交草稿、失败 hook 后刷新、逐行暂存、冲突版本保护及刷新失败后的写入保护。设置 `XSOURCETREE_TEST_SCREENSHOTS=/tmp/xsourcetree-native-preview` 可导出浅色、深色和窄窗口截图；该测试不验证系统输入法候选窗。

AI 协议测试使用本地 HTTP 服务；设置交互使用内存凭证替身，不读取用户的密钥。可单独执行 `XSOURCETREE_TEST_KEYCHAIN=1 GOMAXPROCS=2 go test -p=1 -parallel=2 ./internal/aikeychain` 验证原生钥匙串读写；该用例只创建独立服务名下的合成测试记录并在结束时清理，不访问已有记录。

真实桌面端集成测试通过 MyGo 的 automation 插件操作应用。插件仅编译进 `e2e` 构建，并且仅在设置 socket 路径时启用。先启动测试用应用，再在另一个终端执行测试：

```sh
GOMAXPROCS=2 GOFLAGS="-p=1 -tags=e2e" \
  MYGO_AUTOMATION=/tmp/xsourcetree-e2e.sock bun run dev
```

```sh
MYGO_AUTOMATION=/tmp/xsourcetree-e2e.sock node scripts/native-smoke.mjs
```

测试先确认窗口类型为原生内容，再验证中文路径添加、中文提交说明、暂存与菜单提交、历史工作区的整文件／分组／区块／逐行暂存及取消暂存、并排 Diff、分支创建与切换、标签、历史搜索、远程配置及首次发布、贮藏、备份后丢弃、原生菜单与分组。推送目标全部为临时本地 bare 仓库。启用自动化时，测试应用使用独立的临时数据目录，退出时清理，不影响普通开发版或正式版的本地仓库列表及恢复副本。自动化输入通过框架内容事件发送，菜单快捷键仍需真实键盘验证。

单实例锁按应用名和 Bundle ID 区分，临时数据目录本身不会改变锁。已有开发实例运行时，macOS 可用独立的 e2e 二进制进行上述测试，避免接管原开发实例：

```sh
GOMAXPROCS=2 go build -p=1 -tags=e2e -o /tmp/51code-e2e .
MYGO_LOCALE=zh-Hans MYGO_AUTOMATION=/tmp/xsourcetree-e2e.sock /tmp/51code-e2e
```

## 项目结构

以下路径均相对于仓库根目录：

```text
main.go                       构建信息注入与应用入口
internal/app/app.go            MyGo 生命周期与原生菜单
internal/app/window.go         窗口状态、后台任务与草稿保存
internal/app/ui.go             原生界面布局
internal/app/history.go        提交历史、图谱与引用标签
internal/app/*service.go       界面复用的应用层服务
internal/app/*_test.go         应用与原生界面测试
internal/app/automation_e2e.go 自动化插件及隔离数据初始化，仅 e2e 构建启用
internal/gitrepo/              系统 Git 调用、状态解析与仓库操作，无桌面依赖
internal/workspace/            本地仓库库、扫描、分组与已打开仓库管理，无桌面依赖
internal/commitai/             AI 提交说明协议适配
internal/aikeychain/           原生钥匙串读写
assets/file-icons/             文件图标与许可证的嵌入包
scripts/native-smoke.mjs      真实原生桌面端集成测试
```

`main.go` 保留 `buildNumber`、`buildCommit`、`buildTime` 链接变量，通过 `app.BuildInfo` 传给 `app.Run`，已有 `-X main.*` 构建参数继续有效。应用代码及测试集中在 `internal/app`，暂不按界面页面拆包；`e2e` 初始化随应用包加载。`task test:ui` 与 `bun run test:ui` 指向该包，完整测试仍使用 `go test ./...`。

Git 命令使用参数数组执行，路径按字面值处理。写操作限定在已打开的仓库内，文件变更操作校验当前状态；取消暂存保留工作区内容。命令设有超时，Diff 禁用外部 diff/textconv 程序，输出和预览大小设有限制。窗口关闭会取消该窗口发起的 Git 请求。

仓库现有的 Git 配置、hooks 和签名配置仍然生效。打开仓库应与直接在该仓库运行 Git 一样，信任其配置与 hooks。

## 应用更新与发布

以下命令均在仓库根目录执行。

### Windows x64 与 Linux x64/arm64

首批目标为 Windows 11 x64 和 Linux x64/arm64，回归测试在 Ubuntu 24.04 LTS x64 上运行；arm64 和 RPM 发行版只做交叉构建与包元数据检查，未在真机回归。MyGo 原生界面不需要 WebView2 或 WebKitGTK；Windows 默认使用内置 Git，也可选择系统 Git 或自定义路径。Linux 的 deb 与 rpm 都声明 Git、GTK 3、libsecret 和 Noto CJK 字体依赖：deb 用 Debian 包名，rpm 用 `libsecret-1.so.0()(64bit)` 和 `font(notosanscjksc)` 能力，以兼容不同 RPM 发行版的包名差异（见 `mygo.config.ts`）。Secret Service 由桌面会话提供。CJK 字体用于中文界面及仓库路径，最小系统缺少字体会造成文字缺失和控件测量异常。首次远程操作前应在终端配置好 Git/SSH 凭证和签名；应用继续禁止终端交互提示，没有新增账户登录或凭证配置界面。

`scripts/windows-git.mjs` 固定 MinGit 2.56.1 与 Git LFS 3.8.1 的上游下载地址及 SHA-256。`bun run build` 构建 Windows amd64 时先校验下载缓存，再将完整 MinGit（含 SSH、凭证助手、证书及许可证）和 LFS 暂存到 `resources/windows-amd64/git`；MyGo 将其纳入安装器和更新包，其他平台不包含这些资源，构建结束自动清理。下载缓存位于 `.mygo/git-downloads`。首次构建需要网络；更新版本时同时更新哈希并重新检查布局，不能只复制 `git.exe`。不要绕过包装脚本直接运行 `mygo build` 发布 Windows 包。已存在的暂存目录会阻止构建，须先检查来源。运行时不执行下载或修改系统 PATH，Git 子进程优先使用所选发行包的辅助程序。

`.github/workflows/desktop.yml` 只对 PR 和主分支运行 Windows、Ubuntu、macOS 回归及发布脚本测试，不构建或发布安装包。最多两个独立 runner 并行，各 runner 内的测试串行执行；这不会占用本机的验证预算。Go 使用 `GOMAXPROCS=2`、`-p=1`、`-parallel=2`；Ubuntu 测试使用独立的虚拟显示和 D-Bus/keyring，会话内只存合成测试密钥。各平台保留 `ui.Tester` 浅深色及紧凑布局截图供审阅，这些截图不替代真实系统窗口、输入法及快捷键验收。

三个平台的安装包都在发布用的 Mac 上交叉构建：`task release` 一次 MyGo 调用构建 macOS 双架构、Windows amd64 和 Linux 双架构，共用一个构建号，任一平台失败都不上传、不消耗构建号。Windows 安装器需要 NSIS（`brew install makensis`，确保 `makensis` 在 PATH 中）；deb 与 rpm 由 MyGo 直接生成，无需额外工具。产物位于发布目录的 `windows-amd64/51code-setup-<版本>-amd64.exe`，以及 `linux-<amd64|arm64>/` 下的 `51code_<版本>_<arch>.deb` 和 `51code-<版本>-1.<x86_64|aarch64>.rpm`。Windows 首版未配置 Authenticode 签名，GitHub 提供校验和记录。

三个平台共用同一个签名更新源，各平台读取下载根地址下自己的 `update-<os>-<arch>.json`。MyGo 只在安装目录可写时自更新（`mygo.Updater.Enabled`）：

- Windows 安装器按用户安装到 `%LOCALAPPDATA%\Programs`，可以自更新；原位更新由 Ed25519 验签，不重新运行未签名的安装器。
- Linux 通过 `curl -fsSL https://c.ysicing.net/oss/apps/macOS/51code/install.sh | sh` 安装到 `~/.local`，可以自更新。
- deb/rpm 装在 `/opt`，由包管理器负责，不能自更新；开发构建没有更新源。这些情况下菜单、设置页和右下角版本号都改为打开 GitHub Releases 手动下载，不弹出 MyGo 的“更新不可用”窗口。

安装包（exe、deb、rpm）发布到 GitHub Release，Linux 双架构 deb/rpm 还通过 `fury push` 发布到 Gemfury 的 `ysicing` 组织；不发布 `install.ps1` 和 `SHA256SUMS`，Windows 统一用安装器安装。v0.2.0 及更早的 Windows/Linux 版本没有内嵌更新源，需要手动升级一次。

### 存储与签名

更新包与清单放在自有对象存储，沿用 XStats 的 `mc` 存储别名和 CDN，使用独立目录。[release.config.json](../release.config.json) 统一维护下载根地址 `https://c.ysicing.net/oss/apps/macOS/51code`、上传目标 `c-ip/oss/apps/macOS/51code` 和签名公钥。客户端直接通过 HTTPS 检查与下载，再交由 MyGo 验签安装。存储凭证仅由发布机器的 `mc` 管理。

原 `XSourceTree` 目录（`macOS/XSourceTree`）已停止同步，其中的 macOS 清单固定在 0.2.0 并指向新目录的归档；旧客户端先更新到 0.2.0，再从新地址继续更新。该目录下的清单及 0.2.0 归档不要删除。MyGo 原位替换更新会保留已安装的 `.app` 文件路径，显示名称变为 51Code；通过新 DMG 安装时应用文件名为 `51Code.app`。

同一配置的 `macos` 保存 Developer ID 证书指纹、预期团队 ID 和公证钥匙串配置名。当前复用本机的 `XStats` 公证配置，凭据和证书私钥保存在钥匙串中，不写入仓库。证书续期后须更新指纹；更换发布机器时须安装对应证书私钥并配置同名公证凭据。可用 `security find-identity -v -p codesigning` 检查证书、`xcrun notarytool history --keychain-profile XStats` 检查公证权限。

各平台分别读取下载根地址下的 `update-darwin-<arm64|amd64>.json`、`update-windows-amd64.json` 和 `update-linux-<amd64|arm64>.json`；当前发布完整更新包，不生成增量包。发布脚本要求五个平台的清单与归档齐全，且版本为 `x.y.z` 稳定版。

版本统一修改 [package.json](../package.json) 的 `version`，MyGo 构建配置、关于窗口与界面版本号均使用该版本；同时在 [CHANGELOG.md](../CHANGELOG.md) 添加对应 `## <version>` 节，构建时写入更新说明。

发布签名私钥必须与 `release.config.json` 中的公钥配对；私钥保存在仓库外并离线备份，不要重新生成替换，否则已安装应用将拒绝新签名。当前维护者本机路径为 `~/.config/xsourcetree/update-keys/mygo-update.key`；其他构建环境通过 `MYGO_UPDATER_PRIVATE_KEY` 提供同一私钥。

### 构建与上传

构建 macOS 双架构及 Windows、Linux 正式发布包，完成 Apple 公证和本地校验（不上传对象存储；公证会将安装包提交给 Apple）：

```sh
MYGO_UPDATER_PRIVATE_KEY="$(cat ~/.config/xsourcetree/update-keys/mygo-update.key)" \
  task release -- --dry-run
```

没有更新私钥的普通构建仍可检查和安装由维护者签名的更新，但不生成可发布的更新清单。发布机器需安装 `mc` 并配置 `c-ip` 别名，同时具备配置中的 Developer ID 私钥和公证凭据。还需安装 `gh`，通过 `gh auth status --hostname github.com` 确认发布账户具备 `release.config.json` 中 `githubRepository` 的 Release 写权限。`task dev`、`task build` 不启用正式签名和公证。

该命令使用全新的 `build/release-*` 目录，避免复用旧产物，并输出本次目录。正式上传前，须提交并推送本版本源码和对应的 `v<version>` tag，确认 tag 指向产物对应的源码提交。确认后发布这份已验证的产物，无需重新构建：

```sh
task publish -- --dir build/release-实际目录 --dry-run
task publish -- --dir build/release-实际目录
```

也可直接构建并发布：

```sh
MYGO_UPDATER_PRIVATE_KEY="$(cat ~/.config/xsourcetree/update-keys/mygo-update.key)" \
  task release
```

### 发布校验与失败恢复

发布脚本先验证五个平台的版本、URL、大小与 Ed25519 签名，以及 install.sh 内嵌的下载地址和公钥，再解开 macOS 更新归档，检查其中的应用和 DMG：必须使用指定团队的 Developer ID Application 签名，应用须启用 Hardened Runtime，公证票据和 Gatekeeper 校验都必须通过。`release`、`publish` 和各自的 `--dry-run` 均执行这些检查；ad-hoc、开发证书、错误团队或未公证产物不能上传。

通过校验后，上传带版本号的各平台归档、macOS DMG 及 `<version>/update-*.json` 历史清单。版本对象通过条件 PUT 禁止覆盖；已存在且内容相同可重跑，内容不同必须提升版本，包括从 ad-hoc 改为 Developer ID 重新签名的情况。全部版本对象经 CDN 完整回读并验证 SHA-256 后，才更新五份固定更新清单，最后更新 `install.sh`，逐个回读确认。固定清单和 `install.sh` 设置 `Cache-Control: no-cache,max-age=0,must-revalidate`，CDN 需遵守该响应头，不能强制长缓存。

CDN 与更新清单验证通过后，脚本自动创建或补齐 `githubRepository`（`ysicing/51code`）中对应 `v<version>` 的 GitHub Release，附加同一产物目录中的 `51code-<version>-arm64.dmg`、`51code-<version>-amd64.dmg`、`51code-setup-<version>-amd64.exe`，以及 Linux amd64/arm64 的 deb 和 rpm，共 7 个附件。上传前只核对新平台安装包的文件格式头，防止空文件或误放产物。新 Release 先创建草稿，使用更新清单内的版本说明，确认全部附件在 GitHub 上的 SHA-256 digest 与本地一致后才公开。已有同名且 SHA-256 相同的附件直接复用，缺失附件补传，内容不同或无法校验时停止，绝不覆盖。tag 本身没有附件，正式发版不能仅有 tag 或 GitHub 自动生成的源码压缩包。

GitHub Release 验收后，脚本继续发布 Gemfury。`release.config.json` 的 `gemfuryAccount` 固定目标组织（当前为 `ysicing`），发版机器需安装支持 `--public`、`--json` 和 `--no-input` 的 fury CLI（已验证 1.0.0），并提前通过 `fury login` 或 `FURY_TOKEN` 提供权限。脚本仅上传同一目录中的 Linux amd64/arm64 共 4 个 deb/rpm，执行 `fury push --account ysicing --public --no-input --no-progress <待上传文件…>`，首次创建包时即设为公开；不上传 DMG、exe 或更新归档。已存在的同名安装包必须通过 Gemfury 的 SHA-256 校验后才能跳过，缺失文件补传；上传后回读确认 deb/rpm 包均公开且各安装包 SHA-256 一致。已有私有包需要先在 Gemfury Dashboard 中切换到 `ysicing` 组织，将对应的 `51code` deb/rpm 包设为公开，`--public` 不作为修改已有包权限的保证。

`--dry-run` 展示 Gemfury 上传计划，不调用 fury 或读取远端状态。Gemfury 阶段失败时 CDN 和 GitHub Release 可能已生效，必须保留同一发布产物目录，修复权限或网络后重跑 `bun run publish --dir <原目录>`，只补传缺失文件，不重建、覆盖或删除已发布版本。

同一产品发布须串行执行，各平台及新旧目录的清单并非原子更新；中途失败可使用同一产物目录重跑，脚本拒绝覆盖新版本清单为旧版本。CDN 已生效但 GitHub 阶段失败会明确报告部分完成；保留原目录重跑，不重建同版本包、不回退清单。若上传失败留下同名 `starter` 占位附件，脚本会停止并提示：先在对应 Release 中核实并手动删除该占位附件，再用原产物目录重跑；脚本不自动删除附件，不允许删除已有完整附件来绕过内容冲突。`--dry-run` 仅列出 GitHub 发布计划，不验证远端 tag 或权限，不创建 Release、不上传附件。脚本不会修改存储权限、创建标签或提交推送 Git。

### Homebrew

正式安装入口为 `brew install --cask ysicing/tap/51code`。发布完成后，在 `ysicing/homebrew-tap` 更新 `Casks/51code.rb` 的版本、双架构 DMG URL 和 SHA-256；使用 `app "51Code.app"`，保留 `com.ysicing.xsourcetree` 等旧身份。

tap 根目录的 `cask_renames.json` 保留 `"xsourcetree": "51code"`，兼容旧名称及已有安装的迁移。只维护 `51code` cask，不保留同名旧 cask 文件，也不添加 `deprecate!`。检查新旧名称均解析为 `51code`，运行 `brew style --cask ysicing/tap/51code` 和 `brew audit --cask --online --arch=all ysicing/tap/51code`；验证已有安装迁移时使用 `brew migrate --cask --dry-run ysicing/tap/51code`，避免改动用户应用。

### 清理发布产物

CDN、GitHub Release、Gemfury 和 Homebrew tap 均验收通过后，按构建时记录的路径删除本次 `build/release-*` 目录，并确认清理结果。预演或发布中断时保留原目录供续发。每次清理仅限本次发布目录。
