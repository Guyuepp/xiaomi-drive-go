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

云相册说明:
- `<相册>` 写相册名或 ID。相机是 `1`,截屏是 `2`,私密是 `1000`。
- `backup` 默认备份总览加私密相册,这就是全部照片和视频(私密相册不在总览里)。文件按相册分子目录保存,下载后核对 SHA-1,修改时间设为拍摄日期。本地已有同名且大小相同的文件会跳过,中断后重跑即可。
- 服务端不读取文件里的 EXIF 拍摄日期。`put` 默认按文件修改时间归档,用 `--date` 可以指定拍摄时间。
- `search` 用的是服务端搜索,能搜分类(风景、文档……)、地点、时间、人物和相册名,搜不了文件名和图片里的文字。它先列出服务端给的建议项,选一项后再列出照片;可以在终端里输入编号,也可以用 `--pick` 指定。服务端返回的顺序不是按日期的,所以默认会取完全部结果,按拍摄时间从新到旧显示最新的 100 张,`--limit` 调整张数,`--limit 0` 显示全部;结果多时取完要一会儿(每 100 张一个请求)。`--unsorted` 按服务端顺序边取边显示,够数就停。结果不包含私密相册。
- 按文件名找用 `ls --name`,它在本地过滤全部照片(包括私密相册),照片多时要几分钟。
- 同一相册里上传同名但内容不同的文件时,服务端会把新文件改名为 `名称_<毫秒时间戳>.扩展名`。
- 移动照片后照片 ID 会变。
- 短时间内请求太多时,小米会暂时拒绝这个账号的相册请求(`user is blocked`),等几分钟就会恢复。`backup` 遇到这种情况会停下来。
- 开启了相册端到端加密的账号目前不支持。

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

## 挂载到本地(OpenList)

OpenList 驱动在 [OpenList 的 fork](https://github.com/Guyuepp/OpenList) 里,那里提供包含驱动的完整 OpenList 下载。添加存储时选择驱动 `MiCloud`,`user_id` 和 `pass_token` 用 `micloud login qr` 登录后执行 `micloud export` 获得。

驱动的处理方式:
- 续期得到的 `serviceToken` 自动写回 OpenList 的数据库。
- 服务端不接受 0 字节文件,驱动把空文件作为本地占位保存并照常显示,写入内容后再上传。
- 移动到已有同名项的目录时直接报错,不让服务端自动改名为 `name(1)`。

Linux 上用 systemd 和 rclone 部署的完整示例见 [docs/deploy-linux.md](docs/deploy-linux.md)。

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
