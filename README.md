# micloud

小米云盘(`i.mi.com`)的 Go 客户端库与命令行工具。基于 `docs/` 下对接口的实测笔记实现。

接口设计对齐 [OpenList](https://github.com/OpenListTeam/OpenList) 驱动(`List` / `Link` / `MakeDir` / `Move` / `Rename` / `Copy` / `Remove` / `Put` / `GetRoot`),便于先接入 OpenList;后续若挂载体验不足,再据此实现 rclone backend 或 FUSE 层。

## 结构

```
micloud/                核心库(package micloud)
├── session.go          会话:凭据、持久化、令牌续期、passToken 静默恢复、401 自动重试
├── login.go            登录:Cookie 导入、二维码、短信验证码、sts 换令牌
├── crypto.go           短信登录的 AES-128-CBC + RSA-PKCS1v1.5 加密(与网页端字节一致)
├── transport.go        底层 HTTP、&&&START&&& 前缀处理、Set-Cookie 解析
├── files.go            列目录(自动翻页)、按路径查找、详情、新建目录、重命名、搜索、容量
├── filemanager.go      移动/复制/删除(异步任务轮询,检查 failedRecords)
├── download.go         直链与 Range 下载
├── upload.go           分块上传(并发、秒传短路)、覆盖写(改名+删旧)
├── trash.go            回收站列表/恢复/永久删除
├── name.go             名称校验与非法字符映射
├── qr.go               终端二维码渲染
├── types.go errors.go jsonx.go
├── driver/             OpenList 风格驱动适配(仅依赖核心库,演示映射关系)
└── cmd/micloud/        命令行工具
```

## 命令行

```
go build -o micloud ./cmd/micloud

micloud login qr                 # 扫码登录(推荐,最稳)
micloud login sms <手机号>        # 短信验证码登录
micloud login cookie --user <id> --service <token> [--pass <passToken>]

micloud whoami                   # 容量
micloud ls [路径]
micloud mkdir <路径>
micloud get <路径> [-o 输出]
micloud put <本地文件> <远程目录> [--name 名称]
micloud mv|cp <源> <目标目录>
micloud rename <路径> <新名称>
micloud rm <路径>                 # 移入回收站
micloud link <路径>               # 24 小时直链
micloud search <关键词>
micloud trash ls|restore <id>|purge <id>
```

凭据保存在 `os.UserConfigDir()/micloud/credentials.json`(权限 0600)。

## 作为库使用

```go
store := &micloud.FileStore{Path: credPath}
c := micloud.NewClient(micloud.WithStore(store))

if !c.LoggedIn() {
    ch, _ := c.StartQRLogin(ctx)
    art, _ := micloud.RenderQR(ch.Payload)
    fmt.Print(art)            // 终端里展示二维码
    _ = c.PollQR(ctx, ch)     // 阻塞到手机确认
}
c.StartKeepAlive(ctx)         // 后台每 5 分钟续期 serviceToken

files, _ := c.List(ctx, micloud.Root())
```

## 接入 OpenList

`driver/` 是一个只依赖核心库的参考适配器,方法体与真实 OpenList 驱动一致。接入时把该文件复制到 OpenList 的 `drivers/micloud/`,将本地的 `Obj` / `Link` 换成 `model.Obj` / `model.Link`,补上 `Additional`(保存 `passToken` 等)与 `Config` 样板即可。文件以服务端 ID 标识(`Obj.GetID()` 直接回传给客户端)。

## 安全

- `passToken`(约 30 天、滚动续期)权限远高于 `serviceToken`(约 15 分钟)。只用 0600 文件或系统钥匙串保存,任何日志都不得出现。
- 密码登录**不支持**(涉及风控、二次验证、图形验证码)。

## 已知限制(详见 `docs/`)

- 不能上传空文件(服务端拒绝 0 字节)。挂载时 `touch` 需本地暂存。
- 没有原地覆盖写接口;`Overwrite` 用“上传临时名 → 删旧 → 改名”模拟,**文件 ID 会变**。
- 名称大小写不敏感;`rename`/`mkdir` 后列表有 0.2–0.6 秒延迟。
- 任务型操作(移动/复制/删除)即使 `SUCCESS` 也可能在 `failedRecords` 里有同名冲突,库已检查并返回 `TaskError`。

## 测试

```
go test ./...
```

覆盖:AES 加密与 openssl 字节一致、`eui` 结构、名称校验(字符数/字节数/非法字符)、灵活 JSON 解码(大整数 ID 不丢精度)、记录转换与路径拼接。

真实账号的端到端测试(需要已登录的凭据文件;只在 `/__micloud_probe__` 下新建临时目录,结束后删除,并只永久删除自己创建的文件):

```
MICLOUD_CREDENTIALS=~/.config/micloud/credentials.json go test -tags e2e -run E2E -v .
```
