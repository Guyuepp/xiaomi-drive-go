# 小米云盘 (i.mi.com) 接口总览

这是 `api-notes.md` 的精炼版,按实现需要组织。细节与证据见 `api-notes.md`(带章节号,下文用「笔记 §x」引用)。

接口调研日期:2026-10-31。这些都是非官方接口,小米随时可能修改;下文凡标 **[未验证]** 的都没有实测过。

---

## 0. 范围

| 项目 | 说明 |
| --- | --- |
| 登录方式 | 支持 **Cookie 导入、二维码登录、短信验证码登录** |
| 密码登录 | **不做**(涉及加密、风控、二次验证) |

---

## 1. 基础约定

```
主机:        https://i.mi.com            (业务)      https://account.xiaomi.com   (账号/登录)
鉴权:        Cookie  serviceToken + userId    两个都必须,缺一返回 401
POST 另外:   表单里必须带 serviceToken=<与 Cookie 相同>,否则 401 且不执行
POST 编码:   application/x-www-form-urlencoded; charset=UTF-8
GET 的 ts:   可省略
UA/Referer:  可省略
```

- 文件 ID 是大整数字符串。JSON 里根目录 `parentId` 是数字 `0`,其余是字符串。**统一按字符串处理。**
- 根目录 `parentId=0`。`dir/get?id=0`、`folders/detail?id=0` 对根目录**不可用**。
- 时间是毫秒时间戳。
- 新接口:业务失败时 **HTTP 仍是 200**,看 `code`。
- 旧接口(如 `/find/*`)与未登录:HTTP 401,体是 `{"R":401,"S":"Err","D":"<登录地址>"}`。
- 账号站返回的 JSON 带前缀 `&&&START&&&`,解析前要去掉。

### 常见错误码

| code | 含义 | 常见触发 |
| --- | --- | --- |
| 0 | 成功 | |
| -1 | 未知错误 | 参数类型不对。响应带 `retriable:true` 和"服务维护中"文案,**是误导,不要重试** |
| 401(`R`) | 令牌失效 | HTTP 也是 401 |
| 10002 | 名称非法 | 见 §6 |
| 10008 | 参数错误 | `pageNo=0`、`size=0`(空文件)、`parentId=-1` |
| 10016 | 缺必选参数 | `parentId` 为空串 |
| 11002 | 资源不存在 | 无效 ID;对不在回收站的项 `restore`/`purge` |
| 11005 | 同名冲突 | `rename` 到已有名字;`COPY` 到有同名项的目录(在 `failedRecords` 里) |
| 11003 | 不能复制到当前目录 | `COPY` 到源所在目录 |
| 11010 | 目录进了自己或子孙 | `MOVE`/`COPY` |
| 50005 | 存储出错 | `commit` 顺序错或缺块。`retriable:true` **是误导,不要自动重试** |

KSS 上传节点不用 `code`,而是 HTTP 200 + `{"stat":"..."}`,必须检查 `stat`。

---

## 2. 会话与登录

### 2.1 令牌模型(整个库的核心)

| 凭据 | 寿命 | 用途 |
| --- | --- | --- |
| `serviceToken` | **约 15 分钟**(从签发算) | 访问 `i.mi.com` 的所有接口 |
| `passToken` | **约 30 天,每次使用后顺延**(滚动),不轮换 | 在账号站换新的 `serviceToken` |
| `userId` | 固定 | 与上面两个配对使用 |

需要持久化:**`passToken` + `userId`**(+ `deviceId`)。`passToken` 比 `serviceToken` 敏感得多:用权限 600 的文件或系统钥匙串保存,日志里不得出现。

### 2.2 保活:每 5 分钟续期

```
GET /status/lite/setting?type=AutoRenewal&inactiveTime=10
-> 响应 Set-Cookie 里带新的 serviceToken(和 i.mi.com_slh)
```

- 只有 `/status/lite/setting`、`/status/alldetail`、`/status/lite/profile` 会换发令牌。**列目录、下载等业务接口不会续期。**
- 同一个令牌可以并行续期多次,每次得到的新令牌都有效;旧令牌不会立刻失效,但从签发起约 15 分钟后失效。
- 每 5 分钟续一次,实测维持了 35 分钟(6 次)。更长时间 **[未验证]**。

### 2.3 用 passToken 恢复会话(令牌过期、休眠醒来、进程重启后)

```
1. GET https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN
      -> data.loginUrl
2. GET <loginUrl>&_json=true         Cookie: passToken=<..>; userId=<..>
      -> {"code":0,"securityStatus":0,"location":"https://i.mi.com/sts?..."}
3. GET <location>                    (不要自动跟随重定向)
      -> 302,Set-Cookie 里有新的 serviceToken、userId、i.mi.com_slh、i.mi.com_ph
```

- **判断成功必须看 `code == 0` 且 `securityStatus == 0`**。失败时(错误的 `passToken`、缺 `userId`)响应里**也带 `location`**(指向登录页),只是 `code` 是 70016,且对它发 GET 返回 200、无 `Set-Cookie`。
- 必需 Cookie 只有 `passToken` 和 `userId`。缺 `userId` 会失败。
- 实测可以反复使用同一个 `passToken`,不会被消耗。

### 2.4 登录引导(三种登录方式共用)

```
GET https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN
    -> data.loginUrl
GET <loginUrl>&_json=true
    -> &&&START&&&{"_sign":"..","qs":"..","callback":"..","serviceParam":"{..}","sid":"i.mi.com","code":70016,..}
```

- 不需要任何 Cookie 和凭据。这里的 `code:70016` 只表示"尚未登录",**不是错误**。
- 取出 `_sign`、`qs`、`callback`、`serviceParam`、`sid`,后续步骤原样回传。`callback` 的签名是固定的(同一个值),可以缓存,但建议每次登录重新取。
- 登录流程里用到的客户端标识:
  - `deviceId`:`wb_` + uuid,随机生成并持久化即可。
  - `deviceFingerprint`:32 位十六进制,随机生成并持久化即可(服务端接受随机值)。
  - Cookie:`deviceId=<..>; pass_ua=web`。

### 2.5 二维码登录(推荐的主路径)

不需要加密,不触发验证码,不需要凭据。

```
1. 引导参数(2.4)
2. GET https://account.xiaomi.com/longPolling/loginUrl
       ?_locale=zh_CN&_group=DEFAULT&sid=i.mi.com&qs=<qs>&callback=<callback>&_sign=<_sign>
       &serviceParam=<serviceParam>&showActiveX=false&theme=&needTheme=false&bizDeviceType=
       &_hasLogo=false&_qrsize=240&_dc=<毫秒>&deviceFingerprint=<32hex>
   Cookie: deviceId=<..>; pass_ua=web        X-Requested-With: XMLHttpRequest
   -> {"code":0,"loginUrl":"<二维码内容>","qr":"<PNG 地址>","lp":"<长轮询地址>","timeout":300}
3. 展示二维码:内容就是响应里的 loginUrl 字段(已用 zbarimg 解码核对)。
   库自己画即可,不必下载 qr 图片。
4. GET <lp>      超时设 315 秒;Header: X-Requested-With, Origin: https://account.xiaomi.com;Cookie 同上
   手机 App 扫码并确认后返回:
   -> {"code":0,"securityStatus":0,"userId":..,"passToken":"..","location":"https://i.mi.com/sts?..", ...}
5. GET <location>(不跟随重定向) -> Set-Cookie 里有 serviceToken 等
```

- 手机端用「小米账号」App(设置 → 小米账号 → 扫码登录)或米家 App。
- 二维码 `timeout` 为 300 秒。过期返回 `code:70031`、取消 `POST /longPolling/cancle` **[未验证]**(来自前端代码)。
- 响应里的 `lp` 主机因数据中心而异(出现过 `c3`、`ak`),用响应给的地址。
- 实测扫码后 82 秒返回。长轮询期间连接一直挂着,客户端不要设短超时。

### 2.6 短信验证码登录

```
1. 引导参数(2.4)
2. 发验证码:POST https://account.xiaomi.com/pass/sendServiceLoginTicket
       Header: eui: <EUI>                              (加密规则见 2.7)
       form:   sid=i.mi.com & user=<AES 加密的手机号> & captCode=
       Cookie: deviceId=<..>; pass_ua=web
       -> {"code":0,"data":{"vCodeLen":6,"wt":59},"captchaUrl":null}
          vCodeLen=验证码位数,wt=重发冷却秒数
3. 预检:POST https://account.xiaomi.com/pass/phoneInfo
       form:   sid=i.mi.com & user=<明文手机号> & ticket=<验证码> & policyName=miaccount
       -> {"code":0,"data":{"status":2,...}}        status==2 才继续
4. 登录:POST https://account.xiaomi.com/pass/serviceLoginTicketAuth
       form:   user=<明文手机号> & ticket=<验证码> & _json=true & _locale=zh_CN
               & callback & sid=i.mi.com & deviceFingerprint & qs & _sign & serviceParam
       -> {"code":0,"securityStatus":0,"location":"https://i.mi.com/sts?..",..}
          Set-Cookie: passToken、userId(账号站域名)
5. GET <location>(不跟随重定向) -> Set-Cookie 里有 serviceToken 等
```

- **只有第 2 步加密 `user` 并带 `eui`**;第 3、4 步用明文手机号,不带 `eui`、`policyName`(第 4 步)、`captCode`。
- 手机号:大陆号码直接用 11 位,不带 `+86`。国际号码格式 **[未验证]**。
- 手机号必须是账号绑定的号码。
- 实测没有触发图形验证码(`captchaUrl` 为 null)。触发条件 **[未验证]**。
- 验证码有效期、发送频率限制、输错次数限制 **[未验证]**。
- **"验证码错误"的返回码没测准**:我是在验证码已成功使用之后才提交错误码的,得到 10031,但那更可能表示"会话已用完"。实现时不要把 10031 当成"验证码错误"的确定信号。
- 登录失败时(`phoneInfo` 的 `status != 2`、`securityStatus != 0`),`notificationUrl`(若有)是需要用户去处理的二次验证页面。

### 2.7 加密规则(只在短信登录的"发验证码"一步用到)

```
K       = 16 个随机字符,字符集 A-Za-z0-9!@#$%^&*
user    = base64( AES-128-CBC( 手机号, key=K 的 UTF-8 字节, iv="0102030405060708", PKCS7 填充 ) )
EUI     = base64( RSA-PKCS1v1.5( 公钥, base64(K) ) ) + "." + base64("user")
```

- 公钥(1024 位,X.509 SubjectPublicKeyInfo 的 base64,是公开的前端常量):

```
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCYEVrK/4Mahiv0pUJgTybx4J9P5dUT/Y0PuwMbk+gMU+jrZnBiXGv6/hCH1avIhoBcE535F8nJQQN3UavZdFkYidsoXuEnat3+eVTp3FslyhRwIBDF09v4vDhRtxFOT+R7uH7h/mzmyA2/+lfIMWGIrffXprYizbV76+YQKhoqFQIDAQAB
```

- RSA 是 PKCS#1 v1.5 填充(不是 OAEP),输出 base64。
- 已验证服务端接受:正确的 `eui` 得到业务响应;被破坏的 `eui` 返回 `81004`。
- `base64("user")` 是被加密字段名的逗号分隔列表。
- 本机验证时用 `openssl pkeyutl -encrypt -pubin -pkeyopt rsa_padding_mode:pkcs1` 和 `openssl enc -aes-128-cbc -K <hex> -iv <hex>` 实现过。

### 2.8 Cookie 导入

用户给出 Cookie 时:

- **最少**:`serviceToken` + `userId`。只能用 15 分钟左右,之后靠 2.2 续期;进程停止后就废了。
- **推荐**:同时给 `passToken`(账号站域名 `account.xiaomi.com` 下的 Cookie),就能长期免登录。
- 浏览器里 `serviceToken` 的 Cookie **不是 HttpOnly**(实测 `document.cookie` 能读到)。
- `passToken` 在 `account.xiaomi.com` 域名下。**它是否 HttpOnly、用户能否从开发者工具复制,我没有验证过**(调研中 `passToken` 都是通过扫码/短信登录的响应拿到的,没有从浏览器读过)。因此"让用户手动导出 `passToken`"这条路径 **[未验证]**;稳妥的做法是引导用户走二维码或短信登录来获得 `passToken`。
- 用户的导出文件格式(Cookie 请求头字符串、`cookies.txt`、JSON)库需要自己做解析 **[未决定]**。

---

## 3. 文件操作 API 速查

以下均为 `https://i.mi.com` 下的路径。POST 都要在表单里带 `serviceToken`。

| 操作 | 方法 | 路径 | 关键参数 / 说明 |
| --- | --- | --- | --- |
| 容量 | GET | `/status/alldetail` | `totalQuota`、`used`;也会换发令牌 |
| 列目录 | GET | `/drive/v2/user/folders/children` | `parentId`、`pageNo`(从1)、`limit`、`order`、`reverse`、`type` |
| 搜索 | GET | `/drive/v2/user/search` | `queryString`、`pageNo`、`limit`、`type`、`label`、`order`、`reverse` |
| 目录 ID 转路径 | GET | `/drive/v2/user/records/dir/get` | `id` → `data.path`(如 `a/b/c`,无前导斜杠) |
| 文件详情 | GET | `/drive/v2/user/files/<id>` | `data.storage.downloadUrl`(下载用) |
| 目录详情 | GET | `/drive/v2/user/folders/detail` | `id`,不支持根目录 |
| 新建目录 | POST | `/drive/v2/user/folders/create` | `name`、`parentId`;同名**幂等**,返回已有 ID 并带 `existed:true` |
| 重命名 | POST | `/drive/v2/user/records/rename` | `id`、`newName`;冲突报 11005 |
| 移动/复制/删除 | POST | `/drive/v2/user/records/filemanager` | `operateType`=`MOVE`/`COPY`/`DELETE`,`operateRecords`=JSON 数组;**异步** |
| 查询任务 | GET | `/drive/v2/user/records/taskquery` | `taskId`;`status`=`RUNNING`/`SUCCESS`/`FAILED` |
| 创建上传 | POST | `/drive/v2/user/files/create` | 见 §5 |
| 提交上传 | POST | `/drive/v2/user/files/commit` | 见 §5 |
| 回收站列表 | GET | `/drive/user/trash` | `cursor`(游标分页) |
| 恢复 | POST | `/drive/user/trash/<id>/restore` | |
| 永久删除 | POST | `/drive/user/trash/<id>/purge` | 没有"清空回收站",要逐个 |

### 列目录细节

- `limit` **没有上限**(`100000` 也正常),可以一次读完一个目录。`limit=0` 返回空且 `hasMore:true`。
- `order` 只认 `NAME`、`SIZE`、`SERVICE_TIME`,其他值返回 `-1`(区分大小写)。
- `reverse` 的含义**不统一**:`SIZE`、`SERVICE_TIME` 时 `false`=升序、`true`=降序;**`NAME` 相反**(`false`=降序、`true`=升序)。
- `type` 参数对列目录**基本无效**:只有 `folder` 生效,其他值都返回全部。分类过滤要用搜索接口。
- 目录始终排在文件前面。
- 不存在的 `parentId` 返回成功和空列表(不是错误)。
- 响应:`data.records[]`、`data.hasMore`、`data.allCount`。
- 条目字段:`id`、`parentId`、`name`、`type`、`size`、`createTime`、`modifyTime`、`revision`;文件另有 `sha1`(整个文件)、`label`(扩展名)。
- `type` 取值由服务端按扩展名分类:`folder`、`doc`、`pic`、`video`、`audio`、`other`。

### 搜索细节

- 子串匹配、不区分大小写、搜整个云盘、目录文件都匹配。
- `type` 有效值:`doc`、`pic`、`video`、`audio`、`other`、`folder`。传 `image`、`document` 或其他值返回 `-1`。
- `label` 按扩展名精确过滤,如 `label=txt`。
- 返回的记录字段比列表少(没有 `sha1`、`label`)。
- `limit` 同样没有上限。

### 任务型操作(`filemanager`)

```
POST /drive/v2/user/records/filemanager
form: operateType=MOVE|COPY|DELETE
      operateRecords=[{"id":"..","type":"folder|doc|pic|video|audio|other"[,"parentId":"<目标>"]}, ...]   # 每批 ≤200
-> {"data":{"taskId":"..","interval":200,"expireTime":..}}
GET /drive/v2/user/records/taskquery?taskId=..    # 每隔 interval 毫秒查一次,直到不是 RUNNING
-> {"data":{"status":"SUCCESS|FAILED","errCode":..,"errMessage":"..","failedRecords":[..]}}
```

- `MOVE`/`COPY` 才需要 `parentId`;`DELETE` 不需要。
- **任务 `SUCCESS` 不等于操作成功**:对不存在的 ID 做 `DELETE`/`MOVE`/`COPY`,任务也是 `SUCCESS`。库要先自己确认项目存在。
- `COPY` 遇到同名项:任务仍是 `SUCCESS`,但 `data.failedRecords[]` 里带 `errCode:11005`。**必须检查 `failedRecords`。**
- `MOVE` 遇到同名项:不报错,被移动的项**自动改名**为 `name(1).ext`。
- 目录移到自身或子孙:任务 `FAILED`,`errCode:11010`。
- 复制到文件所在目录:`FAILED`,`errCode:11003`。
- 移动目录时目录 ID 不变;复制出来的项(含目录)是**新 ID**。
- `COPY` 是服务端复制,不用重新上传。网页没有用到它,是直接试出来的。

---

## 4. 下载

**推荐两步**(GET 加 Range,不需要 Cookie):

```
1. GET /drive/v2/user/files/<id>   -> data.storage.downloadUrl
2. GET <downloadUrl>               可加 Range: bytes=a-b
```

- 链接约 24 小时有效(`data.storage.expireAt`)。
- 支持 `Range`,返回 206 + `Content-Range`。已验证:区间读、跨 4 MiB 块边界、`bytes=N-`、`bytes=-N`。
- `bytes=0-99999999999`(越界)返回 200 和整个文件。
- 挂载文件系统时可以把 `read(offset, size)` 直接映射成 `Range`。
- 已验证 9 MiB 文件的整体下载与本地 SHA-1 一致。

网页用的三步(`files/download` → jsonp → POST `meta`)也可用,见笔记 §5.1,但没有必要。

---

## 5. 上传

块大小固定 **4 MiB**(4194304)。已验证 13 字节到 20 MiB 的多种大小,下载内容与本地 SHA-1 一致。

### 流程

```
1. 本地计算:整个文件的 SHA-1;每个 4MiB 块的 SHA-1 与 MD5(最后一块可更小)
2. POST /drive/v2/user/files/create
      form: data=<JSON> & kss=<JSON> & temp=false & isClientUploadThumbnail=false & serviceToken=..
      data = {"fileName","size","sha1","mimeType","channel":"WEB","parentId","localCreateTime","localModifyTime","exifInfo":{}}
      kss  = {"block_infos":[{"sha1","md5","size"},..],"splitSize":4194304}
   响应有两种:
     (a) data.exists == true  -> 秒传,服务端已建好文件,data.id 即文件 ID,流程结束
     (b) data.exists == false -> data.uploadId, data.kss.{node_urls[0], file_meta, block_metas[i].block_meta}
3. 对每个块:
      POST <node_urls[0]>/upload_block_chunk?chunk_pos=0&&file_meta=<FM>&block_meta=<BM>      (注意两个 &)
      Content-Type: application/octet-stream      body = 该块字节      不需要 i.mi.com 的 Cookie
      响应 HTTP 200 + {"stat":"BLOCK_COMPLETED","commit_meta":".."}
4. POST /drive/v2/user/files/commit
      form: uploadId=<UID> & file_meta=<FM> & commit_metas=[{"commit_meta":".."},..]   (按块顺序!)
            & parentId=<ID> & temp=false & isClientUploadThumbnail=false & serviceToken=..
      -> data.id = 新文件 ID
```

### 行为与陷阱

| 场景 | 结果 |
| --- | --- |
| 同内容、同目录、同名再传 | `exists:true`,返回**原来的** ID,不新建 |
| 同内容、不同名/不同目录 | `exists:true`,**新建**文件并返回新 ID(文件级秒传) |
| 同名、**不同内容** | 正常上传;提交时被自动改名为 `name(1).ext`、`name(2).ext`,**不覆盖** |
| 空文件(0 字节) | **不能上传**,任何写法都报 10008 `invalid size` |
| 两个文件共享第一个块 | **没有块级去重**:所有块都要传 |
| `block_metas[i].is_existed == 1` | 该块不用传,响应里直接带 `commit_meta`。出现条件没测到 |
| 块可以并发、乱序上传 | 可以。提交时 `commit_metas` 必须按块顺序 |
| `commit_metas` 顺序错或缺块 | 50005,`retriable:true` **不要重试** |
| 块内容与声明哈希不符 | HTTP 200 + `{"stat":"ERR_BLOCK_CORRUPTED"}`,**必须检查 `stat`** |
| 对同一个 `uploadId` 重复 `commit` | 幂等,返回**同一个**文件 ID |
| `temp=true` | 提交成功但文件不在列表里、查不到详情。**不要用** |

### 块内分片(弱网续传,可选)

一个 4 MiB 块可以拆成更小的请求:

1. 第一片 `chunk_pos=0`,响应 `{"stat":"CONTINUE_UPLOAD","upload_id":"..","next_pos":N,"left_bytes":M}`。
2. 后续每片:`chunk_pos=<上次响应的 next_pos>`,并把 `upload_id` 作为**查询参数**带回。
3. 最后一片返回 `BLOCK_COMPLETED` + `commit_meta`。
4. 不带 `upload_id` 的第二片一律返回 `ERR_INVALID_CHUNK_POS`。

### 覆盖写(没有原地更新接口)

`create` 里加 `id`、`fileId`、`revision`、`overwrite`、`conflictPolicy` 等字段都被忽略。要"覆盖",只能:

1. 上传新内容到临时名(如 `name.tmp-<随机>`)。
2. 把旧文件改名为另一个临时名,**或**直接删除旧文件。
3. 把新文件改名为目标名。

**文件 ID 会变**,旧文件进回收站。历史版本接口 `GET /drive/user/files/<id>/revisions` 只返回当前版本,覆盖写不会累积版本。

> 步骤 2、3 之间有一个"目录里暂时没有这个文件"的窗口;先改名再删除可以缩小这个窗口。

---

## 6. 名称规则

| 规则 | 结果 |
| --- | --- |
| 长度 | 两条限制同时存在:字符数 ≤ **200**;UTF-8 字节数 ≤ 约 **381**(127 个中文通过,128 个被拒,临界在 381–383) |
| 非法字符 | `/ \ : * ? < > | "`、制表符、换行符、`\0` |
| 非法名称 | `.`、`..`、`...`;首尾有空格 |
| 允许 | 以 `.` 开头、以 `.` 结尾、中间多个空格、不间断空格、emoji、中文、`CON` 等 Windows 保留名 |
| 大小写 | **不区分大小写**:创建 `casetest` 时返回已有的 `CaseTest` |

错误码 10002。挂载到操作系统时,库需要在本地先校验,或对非法字符做映射。

---

## 7. 回收站

```
GET  /drive/user/trash?cursor=              -> data.list[]{id,name,size,type,label,modifyTime}, data.hasMore, data.cursor
POST /drive/user/trash/<id>/restore
POST /drive/user/trash/<id>/purge
```

- 删除目录时,**回收站里只出现文件,不出现目录**,目录及其内容随之消失。
- 游标分页:实测 130 项分 3 页,依次 10、100、20 项,取上一页的 `cursor`。页大小是否固定为"首页 10、其后 100"只有这一个样本,**不要依赖**,以 `hasMore` 为准。
- 回收站里没有原路径和原父目录 ID。
- 恢复:
  - 原目录还在 → 回原位,ID 不变。
  - 原目录有同名文件 → 被恢复的文件自动改名 `name(1).ext`。
  - 原目录已被删除 → 服务端**在原路径下重建同名目录(新 ID)**,文件放进去;同一次被删目录里的其他文件恢复时共用这个重建的目录。

---

## 8. 对挂载的影响

| 问题 | 建议 |
| --- | --- |
| 随机读 | 映射成 `Range` 请求,服务端支持 |
| 写后立即读列表 | `rename` 和 `mkdir` 之后列表有 **0.2–0.6 秒**延迟(`rename` 8/8 次读到旧数据,`mkdir` 6/8)。上传、移动、删除是即时的。库内缓存以本地已知结果为准,需要核对时等约 1 秒 |
| 覆盖写 / 部分写 | 没有原地更新。需要本地暂存整个文件,close/fsync 时用"新文件 + 改名 + 删旧"提交;文件 ID 会变 |
| `touch` 空文件 | 服务端不接受 0 字节文件。本地暂存,有内容再上传 |
| 同名冲突 | 上传/移动/恢复冲突时服务端自动加 `(1)`;`rename`/`COPY` 冲突报错。库应在上传前自己检查 |
| 大小写 | 服务端不区分大小写,文件系统层面要避免同名仅大小写不同的项 |
| 非法字符 | 本地先校验 §6 的规则 |
| `mtime`/`ctime` | 列表给 `createTime`、`modifyTime`;上传时可以传 `localCreateTime`、`localModifyTime` |
| 权限/所有者 | 调研中没有在接口里看到相关字段(没有专门查过) |
| 容量 | `/status/alldetail` 的 `totalQuota`、`used` |
| 并发 | 没触发过限流:60 个并发列目录、250 个并发建目录均成功。上限 **[未知]** |
| 休眠/断网 | 令牌 15 分钟失效,用 `passToken` 静默恢复(§2.3)。`passToken` 约 30 天不用才会失效 |

---

## 9. 未验证 / 未知清单

| 项目 | 说明 |
| --- | --- |
| 短信"验证码错误"的返回码 | 见 §2.6。需要再发一条短信,登录前故意输错才能测准 |
| 验证码有效期、发送频率、输错次数限制 | 未测 |
| 图形验证码的触发条件 | 实测没触发过 |
| 国际手机号格式 | 未测 |
| 二维码过期(`70031`)、取消(`/longPolling/cancle`) | 来自前端代码,未测 |
| `passToken` 30 天不用后的失效表现 | 未测 |
| 用户在「登录设备管理」里移除设备后 `passToken` 是否立即失效 | 未测 |
| 令牌续期链跨天、休眠后的行为 | 只测了 35 分钟 |
| 容量用尽时的错误码 | 前端有常量,值未知 |
| 端到端加密(E2EE)开启后的上传 | 当前账号 `e2eeStatus` 为 `close`,`encryptInfo` 参数没测 |
| 大于 20 MiB 的上传、`uploadId` 的有效期 | 未测 |
| `is_existed == 1` 的出现条件 | 没测到 |
| 缩略图接口 | `files/thumbnails`、`create/<id>/thumbnail`,未测 |
| 笔记、相册、联系人等其他业务 | 不在范围内,没有调研 |

---

## 10. 用真实账号测试的守则

接口只能用真实账号验证。下面的做法能保证不破坏账号里的数据:

1. **所有写测试只在一个新建的测试目录里做**(如 `__xxx_probe__`),开始前确认回收站为空,结束后核对根目录与测试前一致。
2. **清理时只删自己创建的 ID**。不要写"清空回收站剩余项"这类循环;回收站开头是空的才能把后来出现的项视为自己的。
3. **测试脚本在 401 时必须直接报错**,不要把 401 响应当数据处理。
4. **令牌与 `passToken` 只写进权限 600 的临时文件**,不在命令行、输出、日志或文档里出现,测完立即删除。
5. **短信登录每次只发一条**,避免触发频率限制。
6. **不要在 `/status/*` 接口之外期待续期**;测令牌寿命时不要用同一个令牌做别的事干扰。
7. 测试会在账号下留下**登录会话**。测完去小米账号「登录设备管理」里确认,不需要的话移除。

---

## 11. 参考文件

- `docs/api-notes.md`:带证据与章节号的详细笔记(本文是它的精炼与修正版,冲突时以本文为准)。
- `docs/probe-scripts/`:调研时写的**探测脚本**,保留作为登录流程的参考实现(Python 3,只用标准库加 `openssl`、`qrencode`、`zbarimg` 命令行):

  | 文件 | 内容 |
  | --- | --- |
  | `qr_login.py` | 二维码登录:引导参数 → 取二维码 → 终端画码 → 长轮询 → 换会话 |
  | `sms_login.py` | 短信登录:`send <手机号>` / `verify <验证码>`,含 AES+RSA 加密的 `openssl` 实现 |
  | `refresh_test.py`、`passtoken_rotation.py` | 用 `passToken` 恢复会话、验证不轮换 |

  **它们不是可直接运行的库**:路径写死为 `/tmp/mi_auth/`,依赖运行时生成的状态文件(已删除),输出只保留结构不保留令牌。用作读代码的参考。
- 文件操作(列目录、上传、下载、移动等)的探测脚本没有保留;对应实现见本仓库源码。
