# xiaomi-drive-go

非官方的小米云盘(`i.mi.com`)Go 客户端库和命令行工具,另有 OpenList 驱动,可以把云盘挂载成本地文件夹。

An unofficial Go client library and CLI for Xiaomi Cloud Drive (小米云盘, `i.mi.com`), plus an OpenList driver for mounting the drive as a local folder.

> **免责声明**:本项目与小米公司无关。所用接口来自对网页版的分析,不是公开 API,小米随时可能修改或限制。请只用于自己的账号,风险自负。
>
> **Disclaimer**: Not affiliated with Xiaomi. The API is reverse-engineered from the web client and may change or break at any time. Use with your own account at your own risk.

## 功能

- **登录**:扫码登录(推荐)、短信验证码登录、Cookie 导入。不支持账号密码登录。
- **长期免登录**:保存 `passToken`(约 30 天,每次使用后顺延),`serviceToken` 过期时自动换新,后台每 5 分钟续期。
- **文件操作**:列目录、按路径查找、搜索、容量、Range 下载、4 MiB 分块并发上传(含秒传)、新建目录、重命名、移动、复制、删除、回收站。
- **云相册**:列相册、按日期和相册列照片、按分类/地点/时间/人物搜索、下载照片和视频(支持 Range)、上传到相册并指定拍摄日期、整库备份、相册的新建/改名/删除、照片的移动/复制/删除、相册回收站。
- **服务端行为的处理**:名称规则本地校验;异步任务即使返回成功也检查 `failedRecords`;移动、复制、重命名冲突时返回明确错误。

## 安装 CLI

从 [Releases](https://github.com/Guyuepp/xiaomi-drive-go/releases) 下载对应平台的压缩包,或者用 Go 安装:

```
go install github.com/Guyuepp/xiaomi-drive-go/cmd/micloud@latest
```

macOS 上第一次运行可能被系统拦下(程序没有经过 Apple 公证),可以执行 `xattr -d com.apple.quarantine micloud`。

## 命令行用法

```
micloud login qr                 # 扫码登录(推荐)
micloud login sms <手机号>        # 短信验证码登录(大陆号码直接写 11 位)
micloud login cookie --user <id> --service <token> [--pass <passToken>]
micloud login cookie --stdin < credentials.json   # 推荐的 Cookie 导入方式
micloud export                   # 打印 user_id 和 pass_token,用于配置 OpenList

micloud whoami                   # 容量
micloud ls [路径]
micloud mkdir <路径>
micloud get <路径> [-o 输出]
micloud put <本地文件> <远程目录> [--name 名称]
micloud mv|cp <源> <目标目录>
micloud rename <路径> <新名称>
micloud rm <路径>                 # 移入回收站
micloud link <路径>               # 直链,约 24 小时有效
micloud search <关键词>
micloud trash ls|restore <id>|purge <id>

micloud gallery albums           # 云相册:列相册
micloud gallery ls [相册] [--from YYYY-MM-DD] [--to YYYY-MM-DD] [--video] [--name 子串]
micloud gallery search <关键词> [--pick 编号] [--limit 张数] [--unsorted]
micloud gallery get <照片ID> [-o 输出]
micloud gallery put <本地文件> <相册> [--date "YYYY-MM-DD HH:MM:SS"]
micloud gallery backup <目录> [--album 相册] [--skip-private] [-j 并发]
micloud gallery mkalbum|rename-album|rmalbum ...
micloud gallery mv|cp <照片ID> <相册>
micloud gallery rm <照片ID>
micloud gallery trash ls|restore <id>|purge <id>
```

扫码用「小米账号」App(设置 → 小米账号 → 扫码登录)或米家 App。凭据保存在 `os.UserConfigDir()/micloud/credentials.json`,权限 0600。

`login cookie --stdin` 从标准输入读取一个 JSON 对象，例如 `{"userId":"<id>","passToken":"<token>"}`，也接受 `serviceToken`。凭据文件应只允许自己读取。此选项不能与 Cookie 参数混用；现有的参数方式仍支持，但令牌会出现在进程参数中。验证失败会保留此前的登录凭据。

`micloud get` 将下载写入同目录的唯一临时文件，完成大小和 SHA-1 校验后才替换目标；中断或校验失败会保留原文件。

云相册说明:
- `<相册>` 写相册名或 ID。相机是 `1`,截屏是 `2`,私密是 `1000`。
- `backup` 默认备份总览加私密相册,这就是全部照片和视频(私密相册不在总览里)。文件按相册分子目录保存,下载后核对 SHA-1,修改时间设为拍摄日期。本地已有同名且大小相同的文件会跳过,中断后重跑即可。
- 服务端不读取文件里的 EXIF 拍摄日期。`put` 默认按文件修改时间归档,用 `--date` 可以指定拍摄时间。
- `search` 用的是服务端搜索,能搜分类(风景、文档……)、地点、时间、人物和相册名,搜不了文件名和图片里的文字。它先列出服务端给的建议项,选一项后再列出照片;可以在终端里输入编号,也可以用 `--pick` 指定。服务端返回的顺序不是按日期的,所以默认会取完全部结果,按拍摄时间从新到旧显示最新的 100 张,`--limit` 调整张数,`--limit 0` 显示全部;结果多时取完要一会儿(每 100 张一个请求)。`--unsorted` 按服务端顺序边取边显示,够数就停。结果不包含私密相册。
- 按文件名找用 `ls --name`,它在本地过滤全部照片(包括私密相册),照片多时要几分钟。
- 同一相册里上传同名但内容不同的文件时,服务端会把新文件改名为 `名称_<毫秒时间戳>.扩展名`。
- 移动照片后照片 ID 会变。
- 相册读请求默认至少间隔 3 秒，同一个客户端的并发备份共享这个间隔。短时间内请求太多时，小米可能暂时拒绝相册请求(`user is blocked`)；收到此错误后，本客户端相册 GET 冷却 5 分钟，`backup` 停止工作。该策略不能保证不会触发服务端限制。
- 开启了相册端到端加密的账号目前不支持；状态检查失败或缺失时也会停止操作，稍后调用会重新检查。

## 作为库使用

```
go get github.com/Guyuepp/xiaomi-drive-go
```

```go
import "github.com/Guyuepp/xiaomi-drive-go"

store := &xiaomidrive.FileStore{Path: credPath}
c := xiaomidrive.NewClient(xiaomidrive.WithStore(store))

if !c.LoggedIn() {
    ch, _ := c.StartQRLogin(ctx)
    art, _ := xiaomidrive.RenderQR(ch.Payload)
    fmt.Print(art)         // 在终端显示二维码
    _ = c.PollQR(ctx, ch)  // 阻塞到手机上确认
}
c.StartKeepAlive(ctx)      // 后台续期 serviceToken

files, _ := c.List(ctx, xiaomidrive.Root())
```

v0 阶段接口可能变化。

SDK 的最低 Go 版本仍为 1.24，CI 和发行构建使用受支持的 Go 1.27 最新补丁版本。自行构建也应使用受支持的工具链。

`WithGalleryReadInterval(d)` 可调整相册读取间隔（默认 3 秒，0 关闭主动间隔，封禁后的冷却仍保留）。异步文件操作默认等待两分钟，可用 `WithTaskTimeout(d)` 延长，或设为 0 只使用调用方 context。等待中断会返回包含 task ID 的 `*TaskWaitError`，远端操作可能继续，可用 `Client.WaitTask(ctx, taskID)` 继续查询。

## 挂载到本地(OpenList)

OpenList 驱动在 [OpenList 的 fork](https://github.com/Guyuepp/OpenList) 里,那里提供包含驱动的完整 OpenList 下载。添加存储时选择驱动 `MiCloud`,`user_id` 和 `pass_token` 用 `micloud login qr` 登录后执行 `micloud export` 获得。

驱动的处理方式:
- 续期得到的 `serviceToken` 自动写回 OpenList 的数据库。
- 服务端不接受 0 字节文件,驱动把空文件作为本地占位保存并照常显示,写入内容后再上传。
- 移动到已有同名项的目录时直接报错,不让服务端自动改名为 `name(1)`。

Linux 上用 systemd 和 rclone 部署的完整示例见 [docs/deploy-linux.md](docs/deploy-linux.md)。

本地 `scripts/build-openlist.sh` 使用临时 Go workspace，将 `openlist/` 与当前 SDK 一起构建，包含当前 SDK 修复；单独构建 fork 时仍使用它的 `go.mod` 所指定的 SDK 发行版。

## 已知限制

- 服务端没有原地覆盖写接口。`Overwrite` 用"上传到临时名 → 删除旧文件 → 改名"模拟,**文件 ID 会变**,旧文件进回收站。
- 名称不区分大小写;`rename` 和 `mkdir` 之后列表约有 0.2–0.6 秒延迟。
- 回收站没有"清空"接口,只能逐个永久删除。
- 更多未验证的行为见 [docs/api-summary.md](docs/api-summary.md) 第 9 节。

## 安全

- `passToken` 等同于长期登录凭据,比 `serviceToken` 敏感得多。不要发给别人,也不要贴到 issue、截图或日志里。
- 提交 issue 前请检查日志,去掉其中的令牌、用户 ID 和手机号。
- 不需要时,可以在小米账号的「登录设备管理」里移除对应的登录设备。

## 接口文档

- [docs/api-summary.md](docs/api-summary.md):接口总览,按实现需要组织。
- [docs/api-notes.md](docs/api-notes.md):详细笔记,每条标注"已验证 / 源码 / 未知"。
- [docs/gallery-notes.md](docs/gallery-notes.md):云相册接口笔记。
- [docs/probe-scripts/](docs/probe-scripts/):调研时用的 Python 探测脚本,作为登录流程的参考。

## 开发

```
go test ./...
```

真实账号的端到端测试默认不运行。它只在 `/__micloud_probe__` 下新建临时目录,结束后删除,并且只永久删除自己创建的文件:

```
MICLOUD_CREDENTIALS=~/.config/micloud/credentials.json go test -tags e2e -run E2E -v .
```

## 相关项目

- [wangbjun/go-micloud](https://github.com/wangbjun/go-micloud):较早的交互式命令行客户端,使用账号密码登录,包含相册下载。
- [champlus/xiaomi-cloud-drive](https://github.com/champlus/xiaomi-cloud-drive)(米盘):macOS 客户端,在 Finder 中挂载小米云盘。

## 许可证

[MIT](LICENSE)。OpenList fork 沿用 OpenList 的 AGPL-3.0。
