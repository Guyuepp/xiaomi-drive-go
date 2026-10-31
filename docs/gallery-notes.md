# 小米云相册 (i.mi.com 图库) 接口笔记

抓取日期:2026-10-31。来源:Chrome DevTools 抓包、前端 bundle 静态阅读,以及在已登录浏览器会话里的脚本实测。

本文只讲**云相册**(照片和视频),不是云盘。云盘见 [api-notes.md](api-notes.md)。登录、令牌续期、`passToken` 恢复会话都和云盘共用,这里不重复。

状态标记:
- **[已验证]** 用真实请求测过。
- **[源码]** 只从前端 JS 读到,没有真实调用过。
- **[推断]** 从已有响应推出来的,没有专门的对照实验。
- **[未测]** 没测。

文档里所有令牌、用户 ID、相册 ID、照片 ID、签名、`file_meta`/`commit_meta` 都是占位符。不要把真实值写进仓库。

测试方法:没有碰账号里原有的任何照片。所有写操作都针对测试时新建的相册(名字以 `micloud-probe` 开头)和脚本生成的测试图,测完已删除并彻底删除(回收站、相册列表都核对过没有残留)。因此"读取已有照片的字段"一类的结论,都来自测试图的记录。

---

## 1. 通用约定

| 项目 | 说明 |
| --- | --- |
| 主机 | `https://i.mi.com`,路径前缀 `/gallery/...` |
| 网页入口 | `https://i.mi.com/gallery/h5`,路由 `#/photo`、`#/album`、`#/album/<相册ID>`。`/#/gallery` 和 `/v1#/gallery` 会跳到这里;`/gallery` 本身是 404。回收站是另一个前端:`/gallery/trash?header=true` |
| 认证 | **[已验证]** 和云盘相同:Cookie `serviceToken` + `userId`。缺 Cookie 时 GET 返回 HTTP 401 和旧格式 `{"R":401,"S":"Err",...}` |
| POST 额外要求 | **[已验证]** 表单里必须带 `serviceToken=<与 Cookie 相同的值>`。缺表单字段、缺 Cookie、只带表单不带 Cookie,三种情况都是 HTTP 401 且**响应体为空** |
| POST 编码 | `Content-Type: application/x-www-form-urlencoded; charset=UTF-8` |
| 时间戳 | GET 带 `ts=<毫秒>`。**[已验证]** 不带也成功 |
| `serviceToken` 可读 | **[已验证]** 页面脚本能从 `document.cookie` 读到它(不是 HttpOnly)。网页的回收站前端就是这样取的 |
| 响应格式 | `{"result":"ok","retriable":false,"code":0,"data":{...},"description":"成功","ts":<ms>}`。业务错误时 HTTP 仍是 200,看 `code`。`retriable` 不可信,理由同云盘 |
| ID | 字符串形式的大整数(约 17 位),相册、照片同一个编号空间。`userId` 在部分响应里是数字 |
| 时间字段 | 毫秒时间戳 |
| 未验证的请求头 | `User-Agent`、`Referer`、`Origin` 是否必需 **[未测]**。云盘不需要 |

### 1.1 错误码 [已验证,除标注外]

| `code` | 含义 | 触发场景 |
| --- | --- | --- |
| 0 | 成功 | |
| 10001 | 系统错误 | 多块上传提交时 `commit_metas` 顺序错(`reason: commit upload error`) |
| 10008 | 参数错误 | 上传时缺 `groupId`;上传 0 字节文件(`reason: minAvailable is non-positive: 0`);对不存在的照片 ID 调 `info/delete`(`reason: unknown media type`) |
| 10016 | 缺失必选参数 | 新建相册时 `fileName` 为空(`reason: fileName参数不能为空`) |
| 50008 | (无文案) | 对**从未提交成功**的 `temp` 记录调 `info/delete`。原因是推断 **[推断]** |
| 50012 | 相册不存在 | 删除不存在的相册(`reason: Album not found. userId:..., albumId:...`) |
| 50014 | 相册非空 | 删除含照片的相册(`reason: Can't delete non-empty album`) |
| 50050 | 照片状态不对 | 对已彻底删除(`purged`)的照片取下载地址(`reason: ... status error`) |
| 50051 | 不是媒体 | 对不存在的照片 ID(如 `1`)取下载地址(`reason: userId:...,resource:1 is not a media`) |
| 10008 | 保留相册 | 删除 ID 很小的不存在相册(如 `123`)时 `reason` 是 `Can't delete reserved album`,不是 50012 |
| 503 | 账号被暂时拦截 | `reason: user is blocked.`,HTTP 仍是 200。见第 9 节 |
| 50006、10031 | 移动、复制、删除失败的几种情况 | **[源码]** 网页对这两个码做了静默处理,含义没有触发过 |

KSS 存储节点的错误是 HTTP 200 加 `stat` 字段,见 6.2。

---

## 2. 数据模型

### 2.1 相册 (group)

| 字段 | 说明 |
| --- | --- |
| `albumId` / `id` | 相册 ID。自建相册是 17 位大整数 |
| `name` / `fileName` | 名称。列表接口叫 `name`,新建/改名接口叫 `fileName` |
| `type` | `"group"` |
| `status` | 自建相册为 `"custom"` |
| `mediaCount` | 照片数 |
| `thumbnails` | `[{ "url": "<带签名的缩略图地址>", "orientation": 0 }]` |
| `lastUpdateTime` | 最近更新,毫秒 |
| `namePinyin` | `{sortkey, pinyin, spinyin}`,排序用 |
| `albumType` | 自建相册是 `"unknown"` |
| `tag` | 同步游标,每次修改都会变 |

系统相册 **[已验证,来自网页路由]**:`#/album/1` 相机,`#/album/2` 截屏,`#/album/1000` 私密,`#/album/videos` 是"所有视频"的虚拟相册(由 `album/video` 接口提供数量)。"照片"总览不是相册,上传时没有对应的 `groupId`,见 6.1。

### 2.2 照片 / 视频记录

```json
{"id":"<照片ID>","fileName":"a.jpg","title":"a","type":"image","mimeType":"image/jpeg",
 "groupId":"<相册ID>","sha1":"<小写十六进制>","size":23542,
 "sortTime":1700000000000,"dateTaken":1700000000000,"dateModified":1700000000000,"createTime":1700000000000,
 "exifInfo":{"make":"...","model":"..."},"thumbStatus":{"isClientUploadThumb":false},
 "thumbnailInfo":{"data":"<签名URL>","isUrl":true},"bigThumbnailInfo":{"data":"<签名URL>","isUrl":true},
 "shareId":"<userId>-<groupId>-<照片ID>","sourceUserAgent":"...","status":"custom","tag":"<游标>",
 "isFavorite":false,"isLivePhoto":false,"isUbiImage":false}
```

| 字段 | 说明 |
| --- | --- |
| `type` | `image` 或 `video`。网页上传时按 `mimeType` 是否含 `image` 判断,其余都当视频 **[源码]** |
| `status` | `temp`(已创建未提交,不可见)、`custom`(正常)、`deleted`(在回收站)、`purged`(已彻底删除) |
| `sortTime`、`dateTaken` | **[已验证]** 默认都等于上传请求里的 `dateModified`,**不读文件里的 EXIF 拍摄日期**(测试图的 EXIF `DateTimeOriginal` 设成 2001 年,照片仍出现在上传当天)。想指定拍摄时间有两种办法,见 6.5 |
| `exifInfo` | **[已验证]** 请求里不带时,服务端提交后从文件里解出 `make`、`model`;视频会解出编码、帧率、分辨率等(`codecName`、`rFrameRate`、`imageWidth` ...)。请求里带的 `exifInfo` 只接受一部分字段,见 6.5 |
| `duration` | 视频才有,秒,整数 |
| `videoTranscodingStatus` | 视频才有。刚上传完是 `ERR_VIDEO_TRANSCODING_NOT_COMPLETE`(转码未完成),不影响原文件下载和缩略图。转码完成后的取值 **[未测]** |
| `isLivePhoto` | 部分图片记录有这个字段(账号里 9008 条中有 101 条),**值全是 `false`**。上传时请求里写 `true` 会被存下来,服务端不会自己判断,见 6.6 |
| `originSha1`、`sourcePackage`、`isFavorite`、`isFrontCamera`、`description`、`geoInfo` | 已有照片上出现过的其他字段,出现比例不一,含义未专门验证 |
| `tag` | 同步游标。同一张照片的 `tag` 在创建、提交、移动、删除、恢复时都会变 |
| `thumbnailInfo`、`bigThumbnailInfo` | 有签名的缩略图地址,尺寸分别是 270×270 和 1080×1080。`allitems` 带 `simpleResult=true` 时没有这些字段 |
| `shareId` | 创建时是 `<userId>-<groupId>-<照片ID>`,提交后变成 `<userId>--<照片ID>`。用途 **[未知]** |

---

## 3. 启动与账号

网页启动时按顺序发这些请求:

```
GET  /mic/keybag/v1/getEncInfo?hsid=2&appId=micloud&ts=<ms>     # 端到端加密状态,见第 10 节
GET  /common/check?ts=<ms>
POST /gallery/user/lite/index/prepare                           # 表单只有 serviceToken
GET  /status/lite/profile、/status/lite/alldetail、/status/vip/level、/status/user/videoplay/level
```

- `prepare` **[已验证]**:`data` 是 `{"reason":"","isReady":true,"status":"ready"}`。GET 方法返回 405。账号第一次使用云相册时是否会返回别的 `status` **[未知]**。不调它是否影响其他接口 **[未测]**:本次所有测试都是在网页已经调过它之后做的。库实现时建议登录后先调一次,代价只有一个请求。
- `getEncInfo` **[已验证,仅未开启的账号]**:走普通的 `data` 外壳,字段有 `e2eeStatus`、`maxAppkey{appKeyVersion,encryptAppKeysSize,setAppKeyVersion,setEncryptAppKeys}`、`nonce`、`zone`、`serverSignZone`。未开启端到端加密时 `e2eeStatus` 为 `"close"`,`appKeyVersion` 为 -1。开启后的取值 **[未知]**,库把 `"close"` 以外的非空值都当作已开启,拒绝使用相册。
- `/status/lite/alldetail`、`/status/lite/profile` 的含义和云盘一致,见 api-notes 4.1。
- 令牌续期同云盘:每 5 分钟调 `/status/lite/setting?type=AutoRenewal&inactiveTime=10`。网页每约 2 分钟调一次。

---

## 4. 读取

### 4.1 时间线 [已验证]

```
GET /gallery/user/timeline?ts=<ms>[&albumId=<相册ID>][&onlyVideo=true][&isShared=true]
-> data: {"dayCount":{"20261031":1,"20261030":10,...},"indexHash":<int>}
```

- 键是 `YYYYMMDD`,值是当天的数量。不带 `albumId` 是整个相册库。
- 账号里照片很多时,`dayCount` 会有上千项,一次返回全部。
- 新相册(无照片)返回 `{"indexHash":<int>}`,**没有 `dayCount` 键**。
- 网页点"视频"时用 `onlyVideo=true`,点共享相册时用 `isShared=true`(共享相册 ID 里带 `-`)。共享相册 **[未测]**。

### 4.2 分页列照片 [已验证]

```
GET /gallery/user/galleries?ts=<ms>&startDate=20261031&endDate=20261031&pageNum=0&pageSize=30[&albumId=<ID>][&onlyVideo=true][&isShared=true]
-> data: {"galleries":[<照片记录>...],"isLastPage":true,"indexHash":<int>}
```

| 参数 | 说明 |
| --- | --- |
| `startDate`、`endDate` | `YYYYMMDD`,两端都包含。取一天就两个一样。可以跨月(`20261001`–`20261031` 一次取到整月)。**两个都不带**时返回全部(没有日期限制) |
| `pageNum` | **从 0 开始** |
| `pageSize` | 网页用 30。500 正常。`0` 返回空且 `isLastPage` 为真 |
| `albumId` | 不带是整个相册库。相机相册的 `albumId=1` 能查 |
| 空结果 | 没有 `galleries` 键,`isLastPage` 为真 |
| 排序 | 按 `sortTime` **降序**(新的在前) |

`pageSize=1` 时 `isLastPage` 在还有后续页时是 `false`,翻页正常。网页的做法是先取时间线,再按天并发取每一天的第 0 页。

**按日期分天的时区 [已验证]**:服务端按**东八区(UTC+8)**把 `sortTime` 归到某一天。`sortTime` 为 `2001-02-03T12:00Z` 的照片出现在 `20010203`,`2001-02-03T20:00Z`(东八区已是 2 月 4 日 04:00)的出现在 `20010204`。`timeline` 的 `dayCount` 键和 `galleries` 的 `startDate/endDate` 是同一套规则。

#### 4.2.1 总览是不是全部照片 [已验证]

不带 `albumId`、不带日期,`pageNum` 从 0 翻页(`pageSize=200`):

| 对账项 | 结果 |
| --- | --- |
| 总览翻完的总数 | 8992 条(不含当时的测试相册),最后一页 `isLastPage` 为真。另做了一次不去重的完整扫描,共 9008 条记录 = 8992 + 当时测试相册的 16 条,说明翻页**没有重复、没有遗漏** |
| 时间线 `dayCount` 之和 | 8992,与总览**完全相等**(1179 天) |
| 各相册 `mediaCount` 之和 | 8996 = 总览 8992 + **私密相册 4** |
| 相机相册(`1`)、截屏相册(`2`) | **在总览里**(3058、4310) |
| 视频 | **在总览里**:272 条,分布在相机、截屏等相册里。`onlyVideo=true` 的总览结果也全是视频;"所有视频"数量(`album/video`)= 272,与总览里的视频数相等 |
| **私密相册(`1000`)** | **不在总览里**,也不在时间线里。必须 `albumId=1000` 单独拉:`galleries`、`timeline`、`allitems` 都能查到那 4 项(图片和视频都有),`status` 为 `custom` |
| `albumId=videos` | 不是真实相册,`code:0` 但返回空。取视频用 `onlyVideo=true` 或 `album/video` |
| 每条记录只属于一个相册 | 是(`groupId` 单值);总览按 `groupId` 汇总与各相册 `mediaCount` 逐个相等(私密相册除外) |

所以**完整备份 = 总览 + `albumId=1000`**,不用逐个相册去拉。另外要用相册结构时(按相册建目录),按 `groupId` 分组即可,不必再请求。

翻完 8992 条用 46 次请求、约 163 秒(`pageSize=200`,顺序请求,没并发),单页耗时约 3.5 秒。并发行为 **[未测]**。

### 4.3 增量同步用的列表 [已验证]

```
GET /gallery/allitems?ts=<ms>&groupId=<相册ID>&tag=0&limit=100[&simpleResult=true]
-> data: {"lastPage":true,"syncTag":"<游标>","syncIgnoreTag":"<游标>","content":[...],"group":[]}
```

- `simpleResult=true` 时 `content` 里只有 `fileName`、`id`、`tag`。不带时是完整记录。
- 顺序是按 `tag` 升序(先创建的在前),和 `galleries` 相反。
- `tag` 当同步游标用,`syncTag` 是下一次的起点。**只测了 `tag=0` 和结果少于 `limit` 的情况**。`lastPage` 为 `false` 时怎么翻页、`tag` 非 0 的行为、`limit` 上限 **[未测]**。
- `group` 是子相册,测试时一直为空,用途 **[未知]**。

### 4.4 相册列表 [已验证]

```
GET /gallery/user/album/list?ts=<ms>&pageNum=0&pageSize=20&isShared=false&numOfThumbnails=1
-> data: {"albums":[<相册>...],"isLastPage":false,"indexHash":<int>}

GET /gallery/user/album/video?ts=<ms>&isShared=false
-> data: {"album":{"mediaCount":272,"thumbnails":[...],"userId":...,"lastUpdateTime":...},"indexHash":<int>}
```

- `pageNum` 从 0 开始。用 `pageSize=50` 取 `pageNum=0` 和 `1` 两页,合计得到 33 个相册(账号总数)。翻页终止条件看 `isLastPage`。`isShared=true` 是共享相册 **[未测]**。
- `numOfThumbnails=1` 每个相册返回 1 个缩略图。
- 系统相册:**[已验证]** 相机(`1`)、截屏(`2`)、私密(`1000`)都出现在 `album/list` 里(33 个相册,`mediaCount` 合计 8996,包含私密相册)。顺序 **[未测]**。

### 4.5 导航与搜索 [已验证,字段结构]

```
GET /gallery/user/navigation?ts=<ms>&clientVersion=a&language=b
GET /gallery/user/search?ts=<ms>&clientVersion=a&language=b&query=<词>
-> data: {"expireMs":600000,"sections":[{"title","type","more":{"title","url"},"items":[{"title","count","icon","url","orientation","thumbnailUrl","bigThumbnailUrl"}]}]}
```

- `navigation` 返回人物、地点、分类(`type` 为 `people`、`location`、`tag`)的汇总,是服务端的智能分类。`items[].url` 是 `https://gallery.i.mi.com/result?title=...&type=...` 的 H5 地址,跳转后的数据接口 **[未知]**。网页还有 `/gallery/user/<path>?...` 和 `/gallery/user/...` 形式的动态路径(把 `items[].url` 的路径拼进去再带 `clientVersion`、`language`、`userId`、`pos`、`num`),取分类下照片的方式 **[源码]**,没有调用过。
- `clientVersion`、`language` 网页固定传 `a`、`b`,不传是否可以 **[未测]**。
- `search` **不按文件名搜索**:用 `micloud-probe`(测试图的文件名)搜,返回 `sections` 里只有 `noResult` 和 `guide`。有结果时的结构 **[未测]**。库里想按文件名查,要自己用 `allitems` 或 `galleries` 拉全量再过滤。

### 4.6 缩略图 [已验证]

`thumbnailInfo.data`、`bigThumbnailInfo.data`、相册 `thumbnails[].url` 都是:

```
https://<存储节点>/2/<节点ID>/get_thumbnail?data=<加密串>&w=270&h=270&ts=<ms>&r=0&_cachekey=<md5>&type=0&sig=<签名>
```

| 项目 | 结果 |
| --- | --- |
| Cookie | **不需要**,不带任何 Cookie 就返回 200 的 `image/jpeg`(跨域 `fetch` 带 `credentials: omit` 也成功) |
| 改 `w` 或 `h` | HTTP 400。尺寸在签名里,**不能自己改成别的尺寸** |
| 改或去掉 `sig` | HTTP 400 |
| 有效期 | URL 里的 `ts` 比签发时刻晚约 24 小时 **[推断]** 为过期时间,没有等到过期 |

想要别的尺寸必须再请求列表,拿服务端签好的 URL。服务端只给两档:270 和 1080。

---

## 5. 下载 [已验证]

有两条路径。

### 5.1 路径一:JSONP 两步(网页的"下载"按钮)

```
GET /gallery/storage?ts=<ms>&id=<照片ID>&callBack=<cb>
-> data.url = https://<kss_web 节点>/kss_web/rd?i=<加密串>

GET <data.url>                                  # 不需要 Cookie
-> <cb>({"meta":"<M>","url":"https://<存储节点>/2/<节点ID>/download_file?..."})     # Content-Type: text/html

POST <url>                                      # 不需要 Cookie
Content-Type: application/x-www-form-urlencoded
body: meta=<M>
-> 原始文件字节
```

- 函数名取自 `callBack` 参数,剥掉外壳后解析 JSON。
- 下载到的字节 SHA-1 与记录里的 `sha1` 一致(23 KB 和 9 MiB 的 JPEG 都核对过)。存的是上传时的原始字节。
- **只能 POST 带 `meta`**:对 `url` 直接 GET 返回 HTTP 200 和 `{"msg":"require meta","stat":"ERR_BAD_PARAMS"}`;`meta=bad` 的 POST 返回 400;`url&meta=<M>` 的 GET 返回 400。
- **Range 支持**(POST 时带 `Range` 头):`bytes=0-99` → 206;`bytes=4194300-4194311`(跨 4 MiB 边界)→ 206;`bytes=9437180-` → 206;`bytes=-10` → 206。`Content-Range` 正确,内容与完整下载的对应字节一致。
- **浏览器脚本跨域 POST 到存储节点会被 CORS 拦掉**(`fetch` 抛 `Failed to fetch`)。网页用隐藏 iframe 里的表单 POST 绕开 **[源码]**。库在 Go 里没有这个限制。

### 5.2 路径二:KSS 块下载

```
GET /gallery/image/storage?ts=<ms>&id=<照片ID>
-> data.kss = {"stat":"OK","blocks":[{"sha1":"<块SHA-1>","size":4194304,"urls":["http://<存储节点>/2/<节点ID>/download_block?..."],"idc_name":"401"},...],
               "secure_key":"<32 位十六进制>"}
```

- 地址的协议是 **`http://`**(不是 https)。
- `GET <urls[0]>` 不需要 Cookie,返回的是**密文**,长度等于块大小。
- 解密 **[已验证]**:RC4,密钥是 `secure_key` **按十六进制解码**得到的 16 字节(不是 UTF-8 字节,也不是 base64),**不丢弃前导字节**。解出后每块的 SHA-1 与 `blocks[].sha1` 一致,按顺序拼起来与路径一的完整文件逐字节相等(9 MiB、3 块)。
- 块地址**忽略 `Range`**,总是返回完整的块(200)。
- 块大小固定 4 MiB(4194304),最后一块更小。
- 这条路径比路径一麻烦,但不用 POST、不受 CORS 限制,而且可以并发取块。库里两条都可以,**建议用路径一**,因为 Range 能直接用。

### 5.3 视频

**[已验证]** 视频可以走和图片完全相同的路径,不需要特殊处理。拿账号里两个现有视频(148 KB 和 5 MB,`video/mp4`)实测:

| 项目 | 结果 |
| --- | --- |
| `/gallery/storage?id=` | 对视频可用,JSONP 两步同图片 |
| 下载内容 | 字节数与记录的 `size` 相等,**SHA-1 与记录的 `sha1` 一致**,文件头是 `ftyp`(MP4) |
| Range | `bytes=0-99`、`bytes=N-`(到末尾)、`bytes=-10`、`bytes=4194300-4194311`(跨 4 MiB 块)全部 206 且内容吻合,`Accept-Ranges: bytes` |
| `/gallery/image/storage?id=` | 对视频也可用,返回 KSS 块(5 MB 的视频拆成 4 MiB + 1 MiB 两块) |
| `/gallery/video/storage?id=` | 可用,返回与 `image/storage` **相同结构的 KSS 块**,同样 `secure_key` 十六进制 RC4 解密后与路径一的完整文件逐字节相等 |
| 响应 `Content-Type` | `application/octet-stream`,不是 `video/mp4` |

结论:图片和视频的下载都用 `/gallery/storage`,不用区分 `image/storage` 和 `video/storage`。账号里最大的视频 2.9 GB(`size` 字段),大文件的下载稳定性和链接有效期 **[未测]**。

分享的照片/视频用 `/gallery/share/image/storage?sharedGalleryId=`、`/gallery/share/video/storage?sharedGalleryId=`、`/gallery/share/image/<id>/downloadurl` **[源码]**,没有测。

### 5.4 彻底删除之后 [已验证]

对 `purged` 的照片调 `image/storage` 返回 `code:50050`,`reason` 为 `userId:...,media:... status error`。回收站里 `deleted` 状态的照片能否下载 **[未测]**。

---

## 6. 上传 [已验证]

相册的上传和云盘共用同一套存储节点和 `upload_block_chunk`,但创建和提交的接口不同。块大小固定 4 MiB。已测:23 KB JPEG(浏览器上传)、9 KB(脚本上传)、5 MB 和 9 MiB(3 块)。

### 6.1 创建

```
POST /gallery/user/full
form: data=<JSON>&isClientUploadThumbnail=false&serviceToken=<token>
```

`data`:

```json
{"content":{"type":"image","groupId":"<相册ID>","mimeType":"image/jpeg","fileName":"a.jpg","title":"a",
            "sha1":"<整个文件的 SHA-1>","size":23542,"dateModified":1700000000000},
 "block_infos":[{"blob":{},"size":23542,"md5":"<块 MD5>","sha1":"<块 SHA-1>"}]}
```

- `block_infos` 每个元素对应一个 4 MiB 块,最后一块可以更小。单块文件的块 SHA-1 等于整个文件的 `sha1`。`blob` 是空对象,必须有。
- `title` 是不带扩展名的文件名。视频还有 `duration`(秒,取整),见 6.6。`exifInfo` 网页在端到端加密关闭时不带,服务端自己解析;带的话只接受白名单字段,见 6.5。
- **`groupId` 必填**:不带返回 10008。**没有"不属于任何相册"的上传**。网页在"照片"总览页点上传时会弹相册选择框,必须选一个已有相册。要放进总览又不污染已有相册,就得单独建一个专用相册。
- `dateModified` 默认同时决定 `sortTime` 和 `dateTaken`(2.2 节),旧时间戳有效,`exifInfo.dateTime` 优先级更高,见 6.5。

响应有两种形态。

需要上传:

```json
{"data":{"sortTime":1700000000000,
  "kss":{"stat":"OK","upload_id":"<UID>","block_metas":[{"is_existed":0,"block_meta":"<BM>"}],
         "node_urls":["https://<存储节点>/2/<节点ID>"],"contentCacheKey":"...","file_meta":"<FM>"},
  "content":{"id":"<照片ID>","status":"temp","tag":0,...}}}
```

秒传(服务端已有相同内容):

```json
{"data":{"sortTime":...,"fileExists":true,"content":{"id":"<新照片ID>","status":"custom","tag":"<游标>",...}}}
```

没有 `kss`,**不需要上传块也不需要提交**,记录已经是 `custom`。

| 场景 | 结果 |
| --- | --- |
| 同相册、同名、同内容再传一次 | `isDuplicated:true` 加 `fileExists:true`,返回**原来的照片 ID**(`tag` 会更新),不新建 |
| 同内容、不同名(同一相册) | `fileExists:true`,**新建**记录,返回新 ID |
| 同相册、**同名、内容不同** | **并存,不覆盖,不报错**。创建阶段正常返回 `kss`,提交时服务端把**后来的**文件改名为 `<名>_<毫秒时间戳><扩展名>`(例如 `a_1793452582173.jpg`),第一张保持原名。每次都新建记录。`title` 保持原来的(`a`),只有 `fileName` 被改。**不是云盘的 `name(1).ext`** |
| 同名大小写不同(`a.jpg` 与 `A.JPG`) | 同样视为同名,后者被改名为 `A_<毫秒时间戳>.JPG`。**文件名不区分大小写** |
| 同名、内容与已有的**第一张相同** | 回到秒传:`isDuplicated:true`,返回第一张原来的记录,不新建 |
| 秒传 | 判断依据是整个文件的 SHA-1。是否跨相册、跨用户也秒传 **[未测]** |
| 缺 `groupId` | 10008 |
| 0 字节文件 | 10008,`reason: minAvailable is non-positive: 0`。**不能上传空文件** |
| 文件 `sha1` 与块 SHA-1 对不上 | **创建成功,提交也成功**,服务端直接存下你声明的 `sha1`(测试中声明了 40 个 `1`,之后列表里就是这个值)。**服务端不校验**,库必须自己算对 |
| `size` 与块总大小对不上 | 创建阶段成功,没有往下做 |
| `mimeType` 为 `application/octet-stream` 且文件名为 `.exe` | 创建阶段成功(没有往下做),服务端是否拒绝非图片/视频 **[未测]** |

### 6.2 上传分块

```
POST <node_urls[0]>/upload_block_chunk?chunk_pos=0&&file_meta=<FM>&block_meta=<BM>
Content-Type: application/octet-stream
body: <该块原始字节>
-> {"stat":"BLOCK_COMPLETED","commit_meta":"<CM>"}
```

- 和云盘一样:URL 里是 `chunk_pos=0&&file_meta=`(两个 `&`),不需要 `i.mi.com` 的 Cookie,必须检查 `stat`。
- `is_existed:1` 时该块已经在服务端,不需要上传(云盘里没观察到,相册里也只见过 0)。块级去重的行为 **[未测]**。
- **并发、乱序 [已验证]**:9 MiB、3 块,先发 2 号再发 0、1 号,全部 `BLOCK_COMPLETED`。
- 块内分片续传(`CONTINUE_UPLOAD`、`upload_id`)在云盘里验证过,相册 **[未测]**。
- 浏览器跨域 POST 到存储节点可以(节点返回了 CORS 头),与下载的 POST 不同。

### 6.3 提交

```
POST /gallery/user/full/<照片ID>/storage
form: data={"kss":{"file_meta":"<FM>","commit_metas":[{"commit_meta":"<CM>"},...]}}&serviceToken=<token>
-> data.content: status "custom", tag, createTime, dateTaken, exifInfo ...
```

- **`commit_metas` 必须按块顺序**:顺序错报 **10001** `commit upload error`(云盘里是 50005)。
- 提交成功后 `status` 从 `temp` 变 `custom`。**照片这时才出现在列表里**。
- 创建后没有提交的记录一直是 `temp`,不出现在任何列表里,对它调 `info/delete` 返回 50008。这类记录占不占配额 **[未知]**。块上传要用的 `file_meta`、`block_meta` 只有创建响应里才有,所以不可能"先传块再创建"。库能做的是把这个窗口缩到最小:**创建之前就算好每块的 MD5/SHA-1 并做完本地检查**(所以文件要读两遍或者先缓存),块上传失败时对该块重试。
- 对同一照片 ID 重复提交 **[未测]**。
- 缩略图:网页在端到端加密开启时自己生成缩略图,走 `/gallery/user/full/<id>/create/thumbnail` 和 `.../commit/thumbnail`,创建时 `isClientUploadThumbnail=true` 并带 `encryptInfo`。**[源码]**,当前账号没有开启,没有测。普通情况下 `isClientUploadThumbnail=false`,服务端自己生成缩略图。

### 6.4 上传的完整顺序

```
1. 对每块算 MD5 + SHA-1,对整个文件算 SHA-1
2. POST /gallery/user/full                       -> 秒传则结束
3. 并发 POST <node>/upload_block_chunk ...       -> 记下每块的 commit_meta
4. POST /gallery/user/full/<id>/storage          -> 按块顺序提交
```

### 6.5 控制照片的拍摄日期 [已验证]

照片排在哪一天,由服务端记录的 `sortTime`/`dateTaken` 决定。默认取请求里的 `content.dateModified`。实测了几种写法(都上传到测试相册,时间线、`galleries` 日期范围、`allitems` 都核对过):

| 写法 | 结果 |
| --- | --- |
| `content.dateModified` 设成旧时间戳(2001 年) | **有效**。`sortTime`、`dateTaken` 等于它,照片出现在时间线 2001 年那天,`galleries` 按 2001 年范围能查到。`dateModified` 本身保持你传的值 |
| `content.dateTaken` | **被忽略**(不报错)。服务端仍用 `dateModified`。传 `dateTaken` 为 2002 年、`dateModified` 为当前时间,照片仍在当前时间 |
| `content.date` | 10008 `convert json to gallery info failed.`,数字和字符串都一样 |
| `content.exifInfo.dateTime`,格式 `YYYY:MM:DD HH:MM:SS` | **有效,优先级高于 `dateModified`**。`2003:04:05 12:00:00` 得到 `dateTaken`/`sortTime` = `2003-04-05T04:00:00Z`,即服务端把它当作**东八区本地时间**换算。`dateModified` 仍是上传时刻 |
| `exifInfo.dateTime` 与旧 `dateModified` 同时给 | 以 `exifInfo.dateTime` 为准(`dateModified` 2010、`dateTime` 2012,结果是 2012) |
| `exifInfo.dateTime` 格式不对(`not a date`、ISO 8601) | 创建阶段**不报错**。这两条没有走到提交,最终取什么日期 **[未测]**。库里必须自己保证格式 |
| 文件里自带的 EXIF `DateTimeOriginal` | **不读**,见 2.2 节 |

**时区**:服务端按 UTC+8 分日。`dateModified` 用 UTC 毫秒就行;`exifInfo.dateTime` 要传"东八区的本地钟表时间"。账号设成别的时区时 `exifInfo.dateTime` 是否仍按 UTC+8 **[未测]**。

**`exifInfo` 接受哪些字段**:请求里的 `exifInfo` 是严格的白名单,未知字段会让整个创建请求被拒(10008,`convert json to gallery info failed.`)。

| 结果 | 字段 |
| --- | --- |
| 接受并原样存下 | `dateTime`、`make`、`model`、`imageWidth`、`imageLength`、`orientation`、`flash`、`whiteBalance`、`exposureTime`、`focalLength`(后两个服务端存成字符串,`0.01` → `"0.01"`) |
| **拒绝**(10008) | `dateTimeOriginal`、`DateTimeOriginal`、`fNumber`、`iso`、`software`、`latitude`、`longitude`、`gpsLatitude`、`gpsLongitude`、`gpsAltitude`、`gpsDateStamp` |

上面是我试过的候选名,不是完整白名单。**GPS 位置不能通过 `exifInfo` 带上去**(试过的几种名字都被拒),地理位置信息(记录里的 `geoInfo`)怎么写入 **[未知]**。

对库的结论:`Upload` 提供一个拍摄时间选项。设了就同时填 `dateModified`(UTC 毫秒)和 `exifInfo.dateTime`(东八区钟表时间,冗余但稳妥);没设时用文件修改时间做 `dateModified`,不要用上传时刻,否则备份回来的照片都堆在今天。

### 6.6 视频上传 [已验证]

流程与图片相同(6.1 – 6.3),只是 `content` 里 `type:"video"`、`mimeType:"video/mp4"`,另加 `duration`(秒,整数)。用 14 KB、1 秒的 H.264 MP4 实测:

| 项目 | 结果 |
| --- | --- |
| 创建、传块、提交 | 都正常,记录 `type:"video"`、`status:"custom"` |
| `exifInfo` | 请求里没带,服务端解析后填入:`imageWidth`、`imageLength`、`codecName`、`codecLongName`、`rFrameRate`、`avgFrameRate`、`bitRate`、`formatName`、`startTime` 等(这些是服务端自己写入的,上面 6.5 的白名单限制只针对请求) |
| `videoTranscodingStatus` | `ERR_VIDEO_TRANSCODING_NOT_COMPLETE`。几秒后仍是这个值,转码完成后的取值和耗时 **[未测]** |
| 缩略图 | 服务端自动生成,`thumbnailInfo`/`bigThumbnailInfo` 都存在,取到的是 `image/jpeg`(3.8 KB) |
| 下载 | 与上传字节完全一致(14803 字节) |

### 6.7 实况照片(Live Photo)[部分验证]

账号里没有现成的实况照片:总览 9008 条记录里 101 条带 `isLivePhoto` 字段,**值全是 `false`**,也没有任何指向"配对视频"的字段(`isLivePhoto` 之外没有含 `live`、`motion`、`paired`、`related` 的键)。因此无法从现有数据看到小米手机产出的格式。

用通用的 MicroVideo 格式自己构造了一个(JPEG 的 APP1 XMP 里写 `GCamera:MicroVideo`、`MicroVideoOffset`,MP4 直接拼在 JPEG 末尾,共 24 KB)。实测:

| 项目 | 结果 |
| --- | --- |
| 上传 | 当作普通图片:`type:"image"`、`isLivePhoto` 没有变化(`false`),服务端**不会**从 XMP 或内嵌视频判断出实况 |
| 请求里 `isLivePhoto:true` | 被接受并存下,记录里就是 `true`。没有观察到其他副作用(缩略图仍是静态 JPEG) |
| 下载 | 与上传字节**完全一致**,内嵌的 MP4(`ftyp`)和 XMP 都在 |
| 缩略图 | `image/jpeg`,是静态图 |

结论:这种格式的动态照片在云相册里是**一个文件**,备份时下载一次就拿到图加视频,不需要找第二个文件。**未验证**:小米手机实际用的格式(可能是 Xiaomi 自有的 `MotionPhoto` 标记,也可能是图片加独立的视频文件,服务端对两者是否有特殊处理);`isLivePhoto:true` 在小米官方 App 里会不会触发播放。需要一张真实的小米手机动态照片,拿到后再测。

---

## 7. 相册与照片的修改 [已验证]

所有 POST 都要在表单里带 `serviceToken`。

### 7.1 新建相册

```
POST /gallery/album/full
form: fileName=<名称>&serviceToken=<token>
-> data.content: {"id","tag","fileName","type":"group","status":"custom","createTime","dateModified","dateTaken","namePinyin":{...},"isUbiImage":false}
```

| 场景 | 结果 |
| --- | --- |
| 名称已存在 | **幂等**:不报错,返回已有相册的 `id`,不新建 |
| 名称为空 | 10016 |
| 名称含 `/` | **静默去掉**这个字符(`a/b` 建出来的相册叫 `ab`) |
| 首尾空格 | **静默去掉**(`  x  ` 建出来叫 `x`) |
| 300 个字符 | 接受,原样保存。长度上限 **[未测]** |
| 其他字符 | 网页输入框拒绝 `\ / : * ? " < > | @`,服务端除 `/` 外是否处理 **[未测]** |

### 7.2 相册改名

```
POST /gallery/album/put
form: content={"fileName":"<新名>","id":"<相册ID>","type":"group","status":"custom","dateModified":<ms>}&ckey=<ms>&serviceToken=<token>
-> data: {"ckey":"<原样返回>","content":{...新的相册记录,含新 tag 和 namePinyin}}
```

改名与新建重名的冲突行为 **[未测]**。

### 7.3 删除相册

```
POST /gallery/album/full/<相册ID>/delete
form: tag=0&serviceToken=<token>
-> data: {"tag":"<游标>"}
```

- **只能删空相册**:有照片时 `code:50014`。要先逐张删除照片。库里实现"删相册"要先 `allitems` 列出所有照片,删完(并按需彻底删除)再删相册。
- 不存在的相册 `code:50012`。
- 空相册删除成功后,相册列表里立即消失。

### 7.4 移动照片 [已验证]

```
POST /gallery/image/<照片ID>/move
form: tag=1&albumId=<目标相册ID>&serviceToken=<token>
-> data: {"copyContent":{<目标相册里的新记录,新 id>},"purgeContent":{<原记录,status:"purged">},"isDuplicated":false}
```

- **照片 ID 会变**。服务端实际做的是"复制到目标相册 + 把原记录标成 `purged`"。库里不要把照片 ID 当作永久标识,移动之后以返回的 `copyContent.id` 为准。
- 移动后源相册的 `allitems` 里不再有它,目标相册里有。
- 对同一个源 ID 重复移动:同样 `code:0`,返回同一个 `copyContent`,不产生新记录(幂等)。
- 共享相册用 `toSharedAlbumId` 代替 `albumId`,可带 `recordKey` **[源码]**。
- **视频也走这个接口** **[已验证]**:路径里虽然是 `image`,对视频同样成功,返回新 ID。

### 7.5 复制照片 [已验证]

```
POST /gallery/full/<照片ID>/copy
form: tag=1&toAlbum=<目标相册ID>&serviceToken=<token>
-> data: {"isDuplicated":false,"content":{<新记录,新 id>}}
```

- 复制出**新 ID**,源保留。不需要重新上传。
- 再复制一次同一张到同一个相册:`isDuplicated:true`,返回前一次复制出来的记录,不产生新记录。

### 7.6 删除照片(进回收站) [已验证]

```
POST /gallery/info/delete
form: id=<照片ID>&serviceToken=<token>
-> data: <照片记录,status:"deleted",带 deleteTime>
```

- **软删除**:照片从相册和时间线里消失,进回收站。
- 重复删除同一张:`code:0`,返回同一份记录(幂等)。
- 不存在的 ID:10008 `unknown media type`。
- 未提交的 `temp` 记录:50008。
- 网页每次只传一个 `id` **[源码]**。是否存在批量删除的接口 **[未知]**。

---

## 8. 回收站 [已验证]

回收站是单独的前端(`/gallery/trash`),接口也是另一组:

```
GET  /gallery/deleted_v2?_dc=<ms>&maxTag=0&limit=50
-> data: {"lastPage":true,"syncTag":"<游标>","content":[<照片记录,status:"deleted",带 deleteTime>...]}

POST /gallery/info/recovery      form: id=<照片ID>&tag=<tag>[&uuid=<userId>]&serviceToken=<token>
-> data.content: <照片记录,status:"custom">

POST /gallery/info/purge         form: id=<照片ID>&ckey=<ms>[&uuid=<userId>]&serviceToken=<token>
-> data: {"ckey":"<原样返回>","content":<照片记录,status:"purged">}
```

| 场景 | 结果 |
| --- | --- |
| 恢复 | 回到**原来的相册**,照片 ID 不变,`tag` 变新 |
| 不带 `uuid` 的恢复 | 成功,`uuid` 看起来不是必需的。网页传的是自己的 `userId` |
| 对不在回收站的照片(已经恢复过)再恢复 | `code:0`,返回当前记录(幂等,没有报错) |
| 彻底删除 | `status:"purged"`,从回收站消失,之后取下载地址报 50050 |
| 重复彻底删除 | `code:0`,返回同一份记录(幂等) |
| 回收站分页 | 网页参数是 `maxTag`(游标)和 `limit=50`。只测了 `maxTag=0` 且总数只有 5 到 6 项的情况。**翻页语义 [未测]** |
| 回收站保留期限 | **[未知]** |
| 原相册被删 | 恢复到已删除的相册会怎样 **[未测]**(相册只有空时才能删,所以只有先把照片删进回收站再删相册才会出现) |
| 清空回收站 | 没找到专门的接口,需要逐张 `purge` |

---

## 9. 并发与限流

本次相册测试中的请求量不大(最多 3 路并发上传,几十次读),没有触发限流。上限 **[未知]**,云盘的结论见 api-notes 第 9 节。

**[已验证] 会被暂时拦截**:2026-10-31 用 Go 库测试时,先在约 13 秒内发了约 40 个读请求(相册列表、时间线、几页 `galleries`、两个文件各 5 次下载),接着在约 80 秒内做了一组写操作(建相册、6 次上传、复制、移动、删除、恢复、彻底删除)。期间 `galleries` 和 `album/list` 开始返回 `code:503`、`reason: user is blocked.`(HTTP 200),同时上传、复制、移动、回收站 `deleted_v2` 和云盘接口仍然正常。约 4 分钟后自动恢复。触发阈值和拦截时长 **[未知]**。库把 503 映射成 `ErrBlocked`,不自动重试;批量任务(备份)遇到它应当停下,稍后再跑。

---

## 10. 待办

1. **下载地址和 `meta` 的有效期**:`/gallery/storage` 返回的地址、JSONP 里的 `url`/`meta`、KSS 块地址能用多久、能否反复使用。决定 `Download(offset, length)` 每次都要重新请求 `storage` 还是可以缓存地址,断点续传怎么做。**暂缓,以后再测**(做法:拿到地址后隔 10 分钟、1 小时、几小时再带 `Range` 去 POST)。在测之前,库里按"每次读都重新取地址"或"地址只缓存几分钟"保守处理。
2. **端到端加密(E2EE)**:启动时调用了 `/mic/keybag/v1/getEncInfo?hsid=2&appId=micloud`,当前账号看起来未开启(上传请求里 `isClientUploadThumbnail=false`),开启后的 `encryptInfo`、客户端缩略图路径没有测。
3. **共享相册**:`isShared=true` 的列表、`toSharedAlbumId`、`share/image/*`。账号里没有共享相册可供测试。
4. **真实的小米实况照片**:需要一张手机拍的动态照片,验证 6.7 的"部分验证"。另有 HEIC/HEIF、AVCI:网页上传控件接受这些格式(`image/*, video/*, .avci, .heic, .heif`),没有测。
5. **`exifInfo.dateTime` 格式不对时的最终日期**(6.5):创建阶段不报错,没走到提交。GPS/地理位置怎么写入;`exifInfo` 白名单的完整字段。
6. **增量同步**:`allitems` 的 `tag` 游标翻页、`limit` 上限、`syncIgnoreTag` 的含义;`deleted_v2` 的 `maxTag` 翻页。
7. **`temp` 记录**:创建后没提交成功的记录删不掉(50008)。是否占配额、是否会被服务端定期清理、能否对同一 ID 续传(创建后补传块再提交)。测试中前后留下了十几条这种记录,尚未观察到它们消失。
8. **秒传的范围**:跨相册、跨账号的 SHA-1 去重;块级去重(`is_existed:1`)。
9. **分类下的照片**:`navigation` 返回的人物/地点/分类,点进去的列表接口(`gallery.i.mi.com/result` 对应的数据请求)。
10. **`prepare` 的其他状态**:新账号首次使用时的返回值。
11. **相册的更多边界**:改名重名、长度上限、除 `/` 外的特殊字符,系统相册(相机、截屏、私密)能否上传、改名、删除。
12. **容量用尽**时上传的错误码。
13. **视频**:转码完成后 `videoTranscodingStatus` 的取值和耗时;2.9 GB 级别大文件的上传、下载稳定性。
14. **并发读取**:总览翻页是顺序请求测的(163 秒/8992 条),并发翻页或并发下载的限流上限未测。

---

## 11. 对库设计的影响(汇总)

- 认证与登录:和云盘共用一套。相册没有额外的会话。
- 列表:先 `timeline` 拿每天的数量,再按天用 `galleries` 翻页(`pageNum` 从 0 开始)。全量枚举也可以用 `allitems` 加 `simpleResult=true` 只拿 `id`、`fileName`、`tag`,更省流量。两者的顺序相反。
- 照片 ID 不稳定:**移动会换 ID**(旧 ID 变 `purged`),复制出新 ID,`tag` 每次修改都变。库里缓存时用 `sha1` 加 `fileName` 加 `groupId` 做辅助标识,移动后以响应里的 `copyContent.id` 更新。
- 下载:`/gallery/storage` 取地址,按 JSONP 去壳,再用 POST 带 `meta` 下载,支持 `Range`,SHA-1 能直接对上。GET 不行。
- 缩略图:只能用服务端给的两档签名 URL,不要自己改 `w`、`h`。过期时间约 24 小时 **[推断]**,过期了要重新拉列表。
- 上传:`groupId` 必填,没有"总览"这个目的地;需要提前决定放进哪个相册。先算好所有哈希,再 `create` → 并发传块 → 按块顺序 `storage` 提交。`fileExists:true` 就结束。空文件不能传。
- 服务端不校验 SHA-1 和大小,库一定要自己算对,否则会存下一份"标签是错的"的记录。
- 完整备份:**总览 + `albumId=1000`(私密相册)**。总览已包含图片、视频、相机、截屏相册的全部内容,和时间线、各相册 `mediaCount` 对得上(4.2.1)。私密相册不在总览和时间线里,漏了它备份就不完整。每条记录自带 `groupId`,按相册建目录不用再请求。
- 视频和图片同一条下载路径(`/gallery/storage`,POST 带 `meta`),SHA-1 能对上,`Range` 可用。动态照片(MicroVideo 格式)是一个文件,下载一次即可。
- 失败清理:`create` 之后到 `storage` 提交之前出错,会留下 `temp` 记录,而且删不掉(50008)。`create` 必须在传块之前调用(块的 `block_meta` 来自它的响应),所以只能尽量减少这个窗口:先本地算好所有哈希、检查好文件(非空、名称合法、是图片或视频),再调 `create`,块上传失败时对该块重试。
- 日期:默认取请求里的 `dateModified`,旧时间戳有效。导入/备份回来的照片要把 `dateModified` 设成原拍摄时间,同时在 `exifInfo.dateTime` 里写东八区钟表时间(6.5)。**不要用上传时刻**。文件里自带的 EXIF 日期服务端不读。
- 同名不同内容的上传会并存,后一个被改名为 `<名>_<毫秒时间戳>.<扩展名>`;文件名不区分大小写。备份到本地时按"同一相册内文件名大小写不敏感"去重,且名字不要假设是唯一的。
- 删除相册要先清空。删除照片是软删除,要释放空间还得 `purge`。
- 所有写接口的幂等性:新建相册同名、移动、复制、删除、恢复、彻底删除都是幂等的,重试安全。
- 不要对 `retriable:true` 自动重试(同云盘)。
