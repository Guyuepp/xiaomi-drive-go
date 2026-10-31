# 小米云盘 (i.mi.com) 接口笔记

抓取日期:2026-10-31。来源:Chrome DevTools 抓包、前端 bundle 静态阅读,以及脱离浏览器的脚本实测。

状态标记:
- **[已验证]** 用真实请求测过,包括脱离浏览器复现。
- **[源码]** 只从前端 JS 读到,没有真实调用过。
- **[未知]** 还没有摸清。

文档里所有令牌、用户 ID、文件 ID 都是占位符。不要把真实值写进仓库。

---

## 1. 通用约定

| 项目 | 说明 |
| --- | --- |
| 主机 | `https://i.mi.com` |
| 必需 Cookie | **[已验证]** `serviceToken` 和 `userId`,缺任何一个都是 401。其余 Cookie(`i.mi.com_slh`、`i.mi.com_ph`、`uLocale`、`iplocale`、`i.mi.com_isvalid_servicetoken`、`i.mi.com_istrudev`)都可以省略 |
| 可省略的请求头 | **[已验证]** `User-Agent`、`Referer`、`Origin` 对 `i.mi.com` 都不是必需的 |
| POST 额外要求 | **[已验证]** 表单里必须带 `serviceToken=<与 Cookie 相同的值>`,否则 HTTP 401 且不会执行 |
| POST 编码 | `Content-Type: application/x-www-form-urlencoded; charset=UTF-8` |
| 时间戳 | GET 请求带查询参数 `ts=<毫秒时间戳>`。**[已验证]** 不带 `ts` 也能成功,库里可以省略 |
| 令牌有效期 | 约 15 分钟(从签发时刻算),需要续期,见第 3 节 |
| 新接口响应 | `{"result":"ok","retriable":false,"code":0,"data":{...},"description":"成功","ts":<ms>}`,业务错误时 HTTP 仍是 200,看 `code` |
| 旧接口响应 | `{"R":401,"S":"Err","D":"<登录地址>","E":false,...}`,未登录时 HTTP 401,同时用 `Set-Cookie` 清空客户端的 `serviceToken` |
| 文件 ID | 字符串形式的大整数。JSON 里有时是数字(根目录 `parentId:0`),有时是字符串。库里统一按字符串处理 |
| 根目录 | `parentId=0`。`dir/get?id=0` 和 `folders/detail?id=0` 对根目录不可用 |
| 时间字段 | 毫秒时间戳 |

### 1.1 错误码 [已验证]

| `code` | 含义 | 触发场景 |
| --- | --- | --- |
| 0 | 成功 | |
| -1 | 未知错误 | 参数类型不对(`limit=abc`、`parentId=abc`、不认识的 `order`、不认识的搜索 `type`)。响应里 `retriable:true` 并带"服务维护中"的文案,**这是误导**,不要重试 |
| 401 (`R`) | 未登录/令牌过期 | HTTP 状态也是 401,响应是旧格式 |
| 10002 | 名称非法 | 含非法字符、过长、首尾空格,见 4.5 |
| 10008 | 参数错误 | `pageNo=0`、`parentId=-1`、上传时 `size=0`(reason `invalid size`) |
| 10011 | RPC 错误 | `limit=-1` |
| 10016 | 缺失必选参数 | `parentId` 为空字符串 |
| 11002 | 资源找不到 | 不存在的文件/目录 ID;对不在回收站的项做 `restore`/`purge` |
| 11005 | 同名冲突 | `rename` 到已有的同名文件或目录 |
| 50005 | 存储出错 | `commit` 时块顺序不对或缺块。响应里 `retriable:true`,**不要自动重试** |

任务内错误(`taskquery` 的 `data.errCode`):`11010` / `Copy forbidden for sub directory`,表示把目录移动到自身或它的子孙目录里。

KSS 上传节点的错误是 HTTP 200 加 `stat` 字段,见 6.2 节。

---

## 2. 登录

完整链路共 5 步。

### 2.1 获取登录引导参数 [已验证,无需任何 Cookie 或凭据]

```
GET https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN
-> data.loginUrl = https://account.xiaomi.com/pass/serviceLogin?callback=<CB>&sid=i.mi.com&_locale=zh_CN&_group=DEFAULT

GET <loginUrl>&_json=true
-> &&&START&&&{"_sign":"...","qs":"...","callback":"...","serviceParam":"{...}","sid":"i.mi.com","code":70016,...}
```

响应前缀 `&&&START&&&` 需要去掉再解析 JSON。这里的 `code:70016` 只是"尚未登录"的状态,不是错误。
`_sign`、`qs`、`callback`、`serviceParam`、`sid` 就是下一步需要原样回传的字段。

对 `loginUrl` 不加 `_json=true` 时会返回 302 到网页登录页,参数在 Location 里。

### 2.2 提交账号密码 [加密已验证被服务端接受,真实账号登录 未测]

```
POST https://account.xiaomi.com/pass/serviceLoginAuth2
Content-Type: application/x-www-form-urlencoded; charset=UTF-8
X-Requested-With: XMLHttpRequest
eui: <EUI>
Cookie: deviceId=wb_<uuid>; pass_ua=web; uLocale=zh_CN
```

表单字段:

| 字段 | 值 |
| --- | --- |
| `user` | 账号经 AES 加密后的 base64,见下 |
| `hash` | `MD5(密码)` 的**大写**十六进制 |
| `cc` | 国家码,中国大陆 `+86` |
| `sid` | `i.mi.com` |
| `callback`、`qs`、`_sign`、`serviceParam` | 2.1 的返回值,原样回传 |
| `policyName` | `miaccount`(中国区)或 `globalmiaccount` |
| `captCode` | 验证码结果,不需要验证码时为空 |
| `deviceFingerprint` | 32 位十六进制设备指纹,见下 |
| `_json` | `true` |
| `bizDeviceType`、`theme`、`needTheme`、`showActiveX` | 网页会带,值为空或 `false` |

**加密规则**(来自账号站 `crypto.*.chunk.js`):

1. 生成 16 个随机字符 `K`,字符集 `A-Za-z0-9!@#$%^&*`。
2. `user` = `base64( AES-128-CBC( 账号, key=K 的 UTF-8 字节, iv="0102030405060708", PKCS7 填充 ) )`。
3. `EUI` = `base64( RSA-PKCS1v1.5( 公钥, base64(K) ) )` + `.` + `base64("user")`。后半段是被加密字段名的逗号分隔列表。
4. RSA 公钥(生产环境,1024 位,DER/X.509 的 base64):

```
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCYEVrK/4Mahiv0pUJgTybx4J9P5dUT/Y0PuwMbk+gMU+jrZnBiXGv6/hCH1avIhoBcE535F8nJQQN3UavZdFkYidsoXuEnat3+eVTp3FslyhRwIBDF09v4vDhRtxFOT+R7uH7h/mzmyA2/+lfIMWGIrffXprYizbV76+YQKhoqFQIDAQAB
```

   RSA 实现是 JSEncrypt 默认的 PKCS#1 v1.5,输出 base64。

**验证结果**:用一个明显不存在的假账号提交,服务端对我生成的 `eui` 返回 `70016`(登录验证失败),对被破坏的 `eui` 返回 `81004`。
说明服务端能正确解开 `eui`。没有 `eui`、用明文 `user` 提交,服务端也返回 `70016`,会按旧方式处理。这个旧方式能不能真的登录 **[未测]**,库里应当和网页保持一致,发加密版。
真实密码的完整登录没有测,因为不应当把你的密码交给我。

**设备指纹**:网页用开源库 FingerprintJS 的 `visitorId`,是浏览器特征的哈希。库里不需要复现它,服务端是否校验其内容 **[未测]**。
建议每个客户端实例生成一个固定的随机 32 位十六进制串并持久化,和 `deviceId` 一起保存。

**`deviceId`**:格式 `wb_<uuid>`,首次访问账号站时由服务端下发,也可以自己生成。

### 2.3 响应与二次验证 [已验证,抓包]

响应是 `&&&START&&&{...}`。关键字段:

- `securityStatus == 0`:登录成功,`location` 是下一步要访问的地址(含 `ticket`)。
- 其他:需要二次验证,`notificationUrl` 指向验证页面。

新设备或新环境会触发手机验证:

```
GET  https://account.xiaomi.com/identity/list?sid=i.mi.com&context=<CTX>
POST https://account.xiaomi.com/identity/pass/sms/userQuota
POST https://account.xiaomi.com/identity/auth/sendPhoneTicket   body: retry=0&icode=&_json=true
POST https://account.xiaomi.com/identity/auth/verifyPhone       body: _flag=4&ticket=<短信验证码>&trust=false&_json=true
```

状态靠 `identity_session` Cookie 维持(`Path=/identity`)。`trust=true` 能否免去下次短信验证 **[未测]**。

另有验证码(`captCode`)流程:账号站会加载 `captcha-cdn01.infosec.xiaomi.com` 的 SDK,触发条件和接入方式 **[未知]**。

### 2.4 换取会话 [已验证,抓包]

```
GET account.xiaomi.com/identity/result/check?callback=...           -> 302
GET account.xiaomi.com/pass/serviceLoginAuth2/end?userId=...        -> 302
GET i.mi.com/sts?sign=...&ticket=0&auth=...&nonce=...&_ssign=...    -> 302 Location: https://i.mi.com/
```

最后一步 `Set-Cookie` 下发 `serviceToken`(Domain=.i.mi.com, Secure)、`userId`、`i.mi.com_slh`、`i.mi.com_ph`。

### 2.5 库支持的登录方式

| 方式 | 状态 | 说明 |
| --- | --- | --- |
| Cookie 导入 | `serviceToken` + `userId` 的认证和保活已验证(第 1、3 节) | 至少要 `serviceToken` + `userId`;另给 `passToken` 就能长期免登录(见 2.7),但**用户如何从浏览器导出 `passToken` 没有验证过**(它在账号站域名下,是否 HttpOnly 未知) |
| 二维码登录 | **已实测,完整走通**,见 2.6 | 手机 App 扫码确认,不涉及密码,不涉及加密 |
| 短信验证码登录 | **已实测,完整走通**,见 2.8 | 只有发验证码一步要加密 `user` 和 `eui`,没有触发图形验证码 |
| 账号密码登录 | **不做** | 涉及 `hash`、风控、二次验证、验证码 |

2.1–2.4 的密码登录细节保留作参考。

### 2.6 二维码登录 [已验证]

**不需要登录态,不需要加密,不需要验证码。** 用一个随机的 `deviceId` 和随机的 32 位十六进制 `deviceFingerprint` 就被接受。

```
1. 取引导参数: GET https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN
                GET <loginUrl>&_json=true         -> _sign, qs, callback, serviceParam, sid  (见 2.1)

2. 取二维码:   GET https://account.xiaomi.com/longPolling/loginUrl
                  ?_locale=zh_CN&_group=DEFAULT&sid=i.mi.com&qs=<qs>&callback=<callback>&_sign=<_sign>
                  &serviceParam=<serviceParam>&showActiveX=false&theme=&needTheme=false&bizDeviceType=
                  &_hasLogo=false&_qrsize=240&_dc=<毫秒>&deviceFingerprint=<32位hex>
                Cookie: deviceId=wb_<uuid>; pass_ua=web
                X-Requested-With: XMLHttpRequest
   -> &&&START&&&{"code":0,"loginUrl":"https://<dc>.account.xiaomi.com/longPolling/login?ticket=lp_...&dc=<dc>&sid=i.mi.com&ts=...",
                  "qr":"https://account.xiaomi.com/pass/qr/login?ticket=lp_...&...",
                  "lp":"https://<dc>.lp.account.xiaomi.com/lp/s?k=lp_...","timeout":300,"timeInterval":0,"sl":""}

3. 展示二维码: 二维码内容就是响应里的 loginUrl 字段(已用 zbarimg 解码核对,完全相同)。
                库可以自己用 loginUrl 生成二维码,不必下载 qr 图片。

4. 长轮询:     GET <lp>      Header: X-Requested-With, Origin: https://account.xiaomi.com   Cookie: 上面的 deviceId/pass_ua
                客户端超时设 315 秒。扫码并在手机上确认后返回(实测 82 秒)。
   -> &&&START&&&{"code":0,"result":"ok","securityStatus":0,"userId":<int>,"cUserId":"...","passToken":"...",
                  "ssecurity":"...","psecurity":"...","nonce":<int>,"location":"https://i.mi.com/sts?...","notificationUrl":"","pwd":1,...}

5. 换会话:     GET <location>       -> 302,  Set-Cookie: serviceToken, userId, i.mi.com_slh, i.mi.com_ph;  Location: https://i.mi.com/
```

- 过期:`timeout` 为 300 秒。过期后长轮询返回 `code:70031`(来自前端代码,**未实测**),此时重新走第 2 步取新码。
- 取消:`POST /longPolling/cancle`(来自前端代码,**未实测**)。
- 长轮询响应的 `Set-Cookie` 会下发 `passToken`、`userId`、`cUserId`(账号站域名下),`passToken` 过期时间约 30 天。
- `dc`(数据中心)每次可能不同(实测出现过 `c3` 和 `ak`),`lp` 的主机照响应里的地址用即可。
- 没有触发验证码和二次验证。`securityStatus` 为 0。

库里要持久化的是 `passToken` + `userId`(+ `deviceId`),见 2.7。

### 2.7 用 passToken 恢复会话 [已验证]

`passToken` 是长期凭据。持有它就能在不需要密码、短信、扫码的情况下换出新的 `serviceToken`:

```
1. GET https://i.mi.com/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN      -> loginUrl
2. GET <loginUrl>&_json=true
     Cookie: passToken=<passToken>; userId=<userId>[; deviceId=...; pass_ua=web]
   -> {"code":0,"result":"ok","securityStatus":0,"location":"https://i.mi.com/sts?...","userId":...,"psecurity":"...",...}
3. GET <location>          -> 302,  Set-Cookie: serviceToken, userId, i.mi.com_slh, i.mi.com_ph
```

| 项目 | 结果 |
| --- | --- |
| 必需 Cookie | `passToken` 和 `userId` 缺一不可。只有 `passToken` 返回 `code:70016`,和错误 `passToken`、无 Cookie 的对照组一致 |
| 其余 Cookie | `deviceId`、`pass_ua`、`cUserId` 不是必需的 |
| 新令牌可用性 | 用换出的 `serviceToken` 访问云盘列表,成功(`allCount` 正确) |
| 失败表现 | `code:70016`(登录验证失败)。**失败响应里同样带 `location`(指向登录页)**,所以判断成功必须看 `code == 0` 且 `securityStatus == 0`,不能只看有没有 `location`。对失败响应的 `location` 发 GET 返回 200,没有任何 `Set-Cookie`;成功时是 302 并下发 `serviceToken` |
| 每次换出的令牌 | 都是新的,互不相同,都有效。原扫码得到的令牌不受影响 |
| `passToken` 轮换 | **不轮换**:刷新响应的 `Set-Cookie` 里的 `passToken` 与原值相同 |
| `passToken` 有效期 | `Set-Cookie` 过期时间约 30 天,**每次使用后重新计算**(滚动延期,实测扫码时过期在 08:06:04,第一次刷新后变为 08:10:27)。只要 30 天内用一次就不会失效 |
| 同一个 `passToken` 并行使用 | 可以重复使用,不会被消耗 |

库的做法:持久化 `passToken` + `userId`。`serviceToken` 收到 401 时,先重试一次,再用 `passToken` 静默换新,不必打扰用户。
`passToken` 本身失效(30 天未使用,或用户在小米账号里移除了登录设备)时才提示重新登录。

注意:`passToken` 的权限比 `serviceToken` 高得多,库要用权限 600 的文件或系统钥匙串保存,日志里不得出现。

### 2.8 短信验证码登录 [已验证]

实测完整走通:发验证码 → 提交验证码 → 换会话 → 访问云盘成功。**只有"发验证码"这一步要加密 `user` 和带 `eui` 头**,后两步用明文手机号。

引导参数同 2.1(`_sign`、`qs`、`callback`、`serviceParam`、`sid`)。Cookie 用随机的 `deviceId=wb_<uuid>; pass_ua=web`,`deviceFingerprint` 用随机 32 位十六进制,服务端接受。

```
1. 发验证码  POST https://account.xiaomi.com/pass/sendServiceLoginTicket
              Header: eui: <EUI>                      # 加密规则见 2.2
              form:   sid=i.mi.com & user=<AES 加密的手机号> & captCode=
              -> {"code":0,"result":"ok","data":{"vCodeLen":6,"wt":59},"captchaUrl":null,"securityStatus":0,...}
                 vCodeLen = 验证码位数;wt = 再次发送前要等待的秒数

2. 预检      POST https://account.xiaomi.com/pass/phoneInfo
              form:   sid=i.mi.com & user=<明文手机号> & ticket=<短信验证码> & policyName=miaccount
              -> {"code":0,"data":{"status":2,"phone":"...","nickname":"...","maskedUserId":"...","bindTime":...,
                                   "registerPwd":1,"pwd":1,...}}
                 data.status == 2:验证码有效且账号存在,可以进入第 3 步

3. 登录      POST https://account.xiaomi.com/pass/serviceLoginTicketAuth
              form:   user=<明文手机号> & ticket=<短信验证码> & _json=true & _locale=zh_CN
                      & callback=<callback> & sid=i.mi.com & deviceFingerprint=<32位hex>
                      & qs=<qs> & _sign=<_sign> & serviceParam=<serviceParam>
              -> {"code":0,"securityStatus":0,"location":"https://i.mi.com/sts?...","userId":...,"cUserId":"...",
                  "psecurity":"...","qs":"...","pwd":1,...}
                 Set-Cookie: passToken, userId, cUserId (账号站域名)

4. 换会话    GET <location>      -> 302,Set-Cookie: serviceToken, userId, i.mi.com_slh, i.mi.com_ph (同 2.6 第 5 步)
```

- 手机号格式:大陆号码直接用 11 位,不带 `+86`。国际号码的格式 **[未测]**,前端代码里是 `国家码+号码`。
- 第 3 步请求不需要 `policyName`、`eui`,也不需要 `captCode`。
- 本次测试中**没有触发图形验证码**,`captchaUrl` 为 `null`。触发条件(发送过于频繁、风险账号)**[未知]**。
- 第 1 步到第 3 步的间隔实测 26 秒;验证码有效期 **[未测]**。
- 登录得到的 `passToken` 同样可以用 2.7 的方式恢复会话,实测成功。
- 手机号必须是账号绑定的号码。号码未注册时,`phoneInfo` 的 `data.status` 不是 2(含义:`0` 表示需要注册,来自前端代码,**未实测**)。

**错误码 [未能区分]**:验证码已被成功使用之后,再提交 `000000`、`123` 或同一个验证码,预检都返回 `10031`("用户不允许访问这个API");提交空验证码返回 `10001`("系统错误")。
这几次测试是在会话已经成功消耗之后做的,所以 `10031` 更可能表示"会话已用完",**不能据此判断"验证码错误"的返回码**。要弄清需要再发一条短信,在登录前故意提交错误验证码。

**对库的建议**:
- 把 `wt` 当作"重新发送"按钮的冷却时间。
- 把 `vCodeLen` 当作验证码输入长度。
- 预检 `data.status != 2` 或 `code != 0`、登录 `securityStatus != 0` 时,都当作"登录失败,需要用户重试",并把 `notificationUrl`(如有)交给用户处理。
- 不要对验证码错误自动重试,验证码输错次数过多可能被限制,**[未测]**。

### 2.9 其他登录入口(不做)

账号站还提供支付宝、微信、QQ、微博登录,不在范围内。

---

## 3. 会话保活 [已验证]

`serviceToken` 的有效期约 15 分钟。浏览器靠一个定时请求续期:

```
GET /status/lite/setting?ts=<ms>&type=AutoRenewal&inactiveTime=10    # 网页约每 7 分钟一次
```

响应的 `Set-Cookie` 里带新的 `serviceToken`(和 `i.mi.com_slh`)。

| 项目 | 结果 |
| --- | --- |
| 会换发 `serviceToken` 的接口 | `/status/lite/setting`、`/status/alldetail`、`/status/lite/profile` |
| 不换发的接口 | `/user/status`、`/common/check`、`/drive/v2/user/folders/children`、`/mic/keybag/v1/getEncInfo` 等,**列目录等业务接口不会续期** |
| 旧令牌 | 换发后旧令牌不会立刻失效。从签发时刻算,令牌约 **15.3–15.4 分钟**后失效(两个独立样本:15:06:17 签发的在 15:21:32 前失效,15:10:45 签发的在 15:26:07 前失效,轮询间隔 30 秒) |
| 同一令牌多次续期 | 每次都得到不同的新令牌,且都有效 |
| 失效后的表现 | HTTP 401,旧格式响应,`D` 里带登录地址,`Set-Cookie` 清空 `serviceToken` |

库里的做法:每个会话起一个定时器,每 5 分钟用最新令牌请求一次 `/status/lite/setting`,并用响应里的 `serviceToken` 更新本地保存的值。
收到 401 时先尝试用磁盘上保存的最新令牌重试一次,失败再要求重新登录。
更长时间(小时、天)的续期链能否一直维持 **[未知]**。令牌彻底过期后可以用 `passToken` 静默恢复,见 2.7,所以续期链断了也不需要用户介入。

---

## 4. 账号、容量、列目录、搜索

### 4.1 容量 [已验证]

```
GET /status/alldetail?ts=<ms>
```

`data`:`totalQuota`、`used`(字节)、`totalDetail`、`usedDetail`(按业务分项,如 `Drive`、`GalleryImage`)、`level`。这个接口也会换发令牌。

**[已验证,2026-10-31 库实测]** `usedDetail` 的每一项是对象 `{"size":<字节>,"text":"<分项名>"}`,不是数字。

### 4.2 列目录 [已验证]

```
GET /drive/v2/user/folders/children?ts=<ms>&parentId=<ID>&pageNo=1&limit=100&type=&order=SERVICE_TIME&reverse=true
```

| 参数 | 说明 |
| --- | --- |
| `parentId` | 目录 ID,根目录为 `0`。不存在的 ID 返回成功和空列表,**不是错误** |
| `pageNo` | 从 1 开始,`0` 报 10008 |
| `limit` | **没有上限**:`limit=100000` 在 250 项的目录里一次返回全部。`limit=0` 返回空列表和 `hasMore:true`,`limit=-1` 报 10011 |
| `order` | 只认 `NAME`、`SIZE`、`SERVICE_TIME`,其他值报 `-1`。大小写敏感 |
| `reverse` | 见下 |
| `type` | **基本无效**:只有 `folder` 生效(只返回目录),其余取值(`doc`、`pic`、空等)都返回全部。分类过滤要用搜索接口 |

排序方向 **[已验证]**:

| `order` | `reverse=false` | `reverse=true` |
| --- | --- | --- |
| `NAME` | 降序 | 升序(与另两个相反) |
| `SIZE` | 升序 | 降序 |
| `SERVICE_TIME` | 升序 | 降序 |

目录始终排在文件前面。分页稳定:250 项、`limit=100` 分 3 页,名字不重复、不遗漏。

响应 `data`:`{"records":[...],"hasMore":false,"allCount":20}`。

条目:

```json
{"id":"<ID>","parentId":"<父ID>","name":"<name>","type":"folder","size":0,
 "createTime":1700000000000,"modifyTime":1700000000000,
 "revision":"<rev>","status":"normal","showStatus":"NORMAL","privacyStatus":"NORMAL","appId":"11001"}
```

文件额外有:`sha1`(整个文件)、`label`(扩展名,小写)、`channel`、`localCreateTime`、`localModifyTime`。
`type` 由服务端按扩展名分类:`folder`、`doc`(txt、pdf、docx、csv 等)、`pic`、`video`、`audio`、`other`(md、zip、7z、bin 等)。

### 4.3 目录 ID 转路径 [已验证]

```
GET /drive/v2/user/records/dir/get?ts=<ms>&id=<ID>
-> data: {"path":"a/b/c","records":[{"id":"<ID>","name":"c"}]}
GET /drive/v2/user/folders/detail?id=<ID>        # 目录详情
GET /drive/v2/user/files/<fileId>                # 文件详情,见 6.3
```

`id` 不存在时报 11002;`id=0` 不可用。`path` 是以 `/` 连接的完整路径,不含前导斜杠。库里可以用它把目录 ID 还原成路径。

### 4.4 搜索 [已验证]

```
GET /drive/v2/user/search?ts=<ms>&pageNo=1&limit=100&type=&order=SERVICE_TIME&label=&queryString=<关键词>&reverse=true
```

- 子串匹配,**不区分大小写**,同时匹配目录和文件,范围是整个云盘。
- `limit` 同样没有上限(1000 也正常)。分页用 `hasMore` 和 `allCount`。
- 记录字段比列表少(没有 `sha1`、`label`)。
- `type` 过滤:`doc`、`pic`、`video`、`audio`、`other`、`folder` 有效。`image`、`document`、不认识的值报 `-1`。
- `label` 按扩展名精确过滤(如 `label=txt`)。
- 排序参数与列目录相同。

### 4.5 名称限制 [已验证]

| 规则 | 结果 |
| --- | --- |
| 长度 | **两条限制同时存在**:字符数 ≤ **200**(201 个 ASCII 被拒);UTF-8 字节数 ≤ 约 **381**(127 个中文=381 字节通过,128 个=384 字节被拒,临界值在 381–383 之间)。混合字符的情况没有测 |
| 非法字符 | `/` `\` `:` `*` `?` `<` `>` `|` `"`、制表符、换行符、`\0` |
| 非法名称 | `.`、`..`、`...`;首尾有空格 |
| 允许 | 以 `.` 开头(`.hidden`)、以 `.` 结尾(`end.`)、中间有多个空格、不间断空格、emoji、中文、`CON` 等 Windows 保留名 |
| 大小写 | **不区分大小写**:创建 `casetest` 时返回已有的 `CaseTest` |

错误码 10002,`reason` 为 `invalid name: ...`、`long name: ...` 或 `name with leading/trailing spaces: ...`。
挂载到操作系统时,库需要在本地先校验这些规则,或把非法字符做映射。

---

## 5. 下载 [已验证]

### 5.1 三步下载(网页使用)

```
GET /drive/v2/user/files/download?ts=<ms>&ids=["<fileId>"]&jsonpCallback=<cb>
-> data.downLoads[0].jsonpUrl, .record, .expireAt            # ids 是 JSON 数组字符串

GET <jsonpUrl>                                              # 不需要 Cookie
-> <cb>({"meta":"<MS>","url":"https://<storage-host>/2/<id>/download_file?ts=...&sig=...&_cachekey=..."})

POST <url>                                                  # 不需要 Cookie
Content-Type: application/x-www-form-urlencoded
body: meta=<MS>
```

- 函数名取自 `jsonpUrl` 里的 `cb` 参数。库里剥掉外壳后解析 JSON。
- 对不存在的 ID,第一步返回成功但 `downLoads` 是空数组。
- 链接 24 小时有效,`meta` 可以重复使用。

### 5.2 两步下载(更简单)

```
GET /drive/v2/user/files/<fileId>
-> data.storage.downloadUrl = https://<host>/2/<id>/download_file?attachment=1&fn=<b64>&ct=<mime>&meta=<M>&ts=...&sig=...

GET <downloadUrl>                                           # 不需要 Cookie
```

用 GET 直接下载,支持 `Range`。**[已验证]** 9 MiB 的文件整体下载和 `Range: bytes=0-9` 都正确。库里建议用这条路径。

### 5.3 Range [已验证]

两条路径都支持 `Range`,响应 206 和 `Content-Range`:

| 请求 | 结果 |
| --- | --- |
| `bytes=0-99` | 206,100 字节 |
| `bytes=4194300-4194311`(跨 4 MiB 块边界) | 206,内容正确 |
| `bytes=9437180-`(到文件末尾) | 206,内容正确 |
| `bytes=-10`(最后 10 字节) | 206,内容正确 |
| `bytes=0-99999999999`(超出范围) | 200,返回完整文件 |

挂载文件系统时可以直接把 `read(offset, size)` 映射成 `Range` 请求。

---

## 6. 上传 [已验证]

块大小固定为 4 MiB(4194304)。已测的文件大小:22 字节、13 B、1 MiB、4 MiB、5 MiB、8 MiB、9 MiB、12 MiB、20 MiB,哈希和下载内容都与本地一致。

### 6.1 创建上传

```
POST /drive/v2/user/files/create
form: data=<JSON>&kss=<JSON>&temp=false&isClientUploadThumbnail=false&serviceToken=<token>
```

`data`:

```json
{"fileName":"a.txt","size":22,"sha1":"<整个文件的 SHA-1 小写十六进制>","mimeType":"text/plain",
 "channel":"WEB","parentId":"<目录ID>","localCreateTime":1700000000000,"localModifyTime":1700000000000,"exifInfo":{}}
```

`kss`:

```json
{"block_infos":[{"sha1":"<块 SHA-1>","md5":"<块 MD5>","size":4194304}, ...],"splitSize":4194304}
```

- `block_infos` 每个元素对应一个 4 MiB 块,最后一块可以更小,字段是该块的 `sha1`、`md5`(小写十六进制)和 `size`。单块文件的块 SHA-1 等于整个文件的 `sha1`。
- `mimeType` 可传 `application/octet-stream`,服务端按扩展名分类。
- **不能上传空文件**:任何写法(无块、0 字节块、`temp=true`)都返回 10008 `invalid size`。挂载时的 `touch` 需要另行处理,例如本地暂存,有内容再上传。

响应有两种形态:

```json
{"data":{"exists":false,"uploadId":"<UID>",
  "kss":{"stat":"OK","block_metas":[{"is_existed":0,"block_meta":"<BM>"}],
         "node_urls":["https://<kss-host>/2/<node-id>"],"contentCacheKey":"...","file_meta":"<FM>"}}}
```

```json
{"data":{"exists":true,"id":"<文件ID>","uploadId":"<UID>"}}
```

`exists:true` 是**文件级秒传**:服务端已有相同内容(SHA-1 和块哈希一致)。此时服务端**已经创建好了文件**并返回 `id`,不需要上传分块也不需要 `commit`。

| 场景 | 结果 |
| --- | --- |
| 同目录、同名、同内容再传一次 | `exists:true`,返回**原来的文件 ID**,不新建 |
| 同内容、不同名(或不同目录) | `exists:true`,**新建文件**并返回新 ID |
| 同目录、同名、**不同内容** | 正常上传,提交时服务端把新文件自动改名为 `name(1).ext`、`name(2).ext`,**不覆盖** |
| 两个文件共享第一个块 | **没有块级去重**:`is_existed` 仍为 0,所有块都要传 |

### 6.2 上传分块

```
POST <node_urls[0]>/upload_block_chunk?chunk_pos=0&&file_meta=<FM>&block_meta=<BM>
Content-Type: application/octet-stream
body: <该块原始字节>
```

- URL 里是 `chunk_pos=0&&file_meta=`(两个 `&`),网页就这么发,保持一致即可。
- **不需要 `i.mi.com` 的 Cookie**,凭 `file_meta` 和 `block_meta`。
- 响应 HTTP 200 加 JSON,**必须检查 `stat`**:

| `stat` | 含义 |
| --- | --- |
| `BLOCK_COMPLETED` | 该块完成,响应里有 `commit_meta` |
| `CONTINUE_UPLOAD` | 块内分片上传中,见下 |
| `ERR_BLOCK_CORRUPTED` | 块内容与声明的哈希不一致 |
| `ERR_INVALID_CHUNK_POS` | 分片偏移不对 |

**块内分片**(一个 4 MiB 块拆成更小的请求,适合弱网续传)[已验证]:

1. 第一片 `chunk_pos=0`,响应 `{"stat":"CONTINUE_UPLOAD","upload_id":"<长字符串>","next_pos":2097152,"left_bytes":2097152}`。
2. 后续片把 `upload_id` 作为**查询参数**带回,`chunk_pos` 取上一次响应的 `next_pos`(字节偏移)。
   不带 `upload_id` 时一律返回 `ERR_INVALID_CHUNK_POS`。
3. 最后一片返回 `BLOCK_COMPLETED` 和 `commit_meta`。

**并发与顺序** [已验证]:块可以并发、乱序上传(5 路并发、顺序 4,2,0,3,1 均成功)。20 MiB 文件,顺序上传 1.4 MiB/s,5 路并发 2.6 MiB/s(受测试网络限制)。

### 6.3 提交

```
POST /drive/v2/user/files/commit
form: uploadId=<UID>&file_meta=<FM>&commit_metas=[{"commit_meta":"<CM>"},...]&parentId=<ID>&temp=false&isClientUploadThumbnail=false&serviceToken=<token>
```

- `commit_metas` 必须**按块顺序**。顺序错或缺块都返回 50005(`retriable:true` 但不要重试)。
- 响应 `data.id` 是新文件 ID。
- 对同一个 `uploadId` 重复提交是幂等的:第二次也返回 `code:0` 和**同一个文件 `id`**,不会产生重复文件。
- `temp=true` 的提交会成功,但文件**不出现在列表里,也查不到详情**(11002)。用途 **[未知]**,库里不要用。

### 6.4 文件详情 [已验证]

```
GET /drive/v2/user/files/<fileId>?ts=<ms>
```

`data` 是文件记录,另有 `storage.downloadUrl`(见 5.2)、`storage.expireAt`、`storage.kss.blocks`(含每块的 `sha1`、`size`、下载 URL)和 `storage.kss.secure_key`。

### 6.5 覆盖写 [已验证:没有]

没有原地更新内容的接口。`create` 的 `data`/表单里加 `id`、`fileId`、`revision`、`baseRevision`、`overwrite`、`conflictPolicy` 等字段都被忽略,响应与不带时完全一致,提交后产生 `name(1).ext`。

库里实现"覆盖写"的方法,按"先改名再删除"的顺序,避免出现目录里暂时没有这个文件的窗口:

1. 上传新内容到一个临时名字(如 `a.txt.tmp-<随机>`)。
2. 把旧文件改名为另一个临时名,或直接删除旧文件。
3. 把新文件改名为目标名。

**文件 ID 会变**,已删除的旧文件进入回收站。历史版本接口 `GET /drive/user/files/<id>/revisions` 只返回当前版本,覆盖写产生的是新文件,不会累积到同一个 ID 的版本里。

---

## 7. 新建目录、重命名、移动、删除 [已验证]

所有 POST 都要在表单里带 `serviceToken`。

### 7.1 新建目录

```
POST /drive/v2/user/folders/create
form: name=<名称>&parentId=<父目录ID>&serviceToken=<token>
```

响应 `data`:`{"id","name","type":"folder","parentId","createTime","modifyTime","status":"normal"}`。

- **幂等**:同名目录已存在时不报错,返回已有目录的 `id` 并带 `"existed":true`。
- 名称规则见 4.5。

### 7.2 重命名

```
POST /drive/v2/user/records/rename
form: id=<ID>&newName=<新名称>&channel=WEB&serviceToken=<token>
```

- 响应 `{"result":"ok","reason":"success","code":0}`,没有 `data`。`channel` 字段可省略。
- 目录和文件都可以重命名,目录里的内容通过原 ID 仍然可达。
- 新名字与同目录下的任意同名项(文件或目录)冲突时报 **11005**。

### 7.3 批量操作(移动、删除),异步任务

```
POST /drive/v2/user/records/filemanager
form: operateType=MOVE|DELETE&operateRecords=<JSON 数组>&serviceToken=<token>
```

- `DELETE`:`[{"id":"<ID>","type":"folder|doc|pic|video|audio|other"}]`
- `MOVE`:`[{"id":"<ID>","type":"...","parentId":"<目标目录ID>"}]`
- 每批最多 200 项(前端代码)。
- 响应 `{"data":{"taskId":"<id>","interval":200,"expireTime":<ms>}}`。对不存在的 ID 同样返回 `taskId`,之后任务查询得到 `SUCCESS`。
- 轮询 `GET /drive/v2/user/records/taskquery?ts=<ms>&taskId=<id>`,`data.status` 为 `RUNNING`、`SUCCESS` 或 `FAILED`。`FAILED` 时有 `errCode`、`errMessage`;`MOVE` 失败时可能有 `failedRecords`。实测一次轮询就完成。
- **任务成功不代表操作成功**:对不存在的 ID 做 `DELETE` 或 `MOVE`,任务也是 `SUCCESS`。库要先自己确认项目存在。
- `MOVE` 目录到自身或子孙目录:任务 `FAILED`,`errCode:11010`。
- `MOVE` 到已有同名项的目录:不报错,被移动的项**自动改名**为 `name(1).ext`。
- 移动目录时,目录 ID 不变,里面的内容跟着走。

`COPY` **[已验证]**,参数与 `MOVE` 相同(`parentId` 是目标目录):

```
operateType=COPY&operateRecords=[{"id":"<ID>","type":"doc|folder|...","parentId":"<目标目录ID>"}]
```

- 复制出**新 ID**,内容一致,源保留。目录是递归复制,目录 ID 也是新的。
- 服务端复制,不需要重新上传。
- 目标目录里有同名项:任务仍是 `SUCCESS`,但 `data.failedRecords[]` 里带 `errCode:11005`,**不会自动改名**(与 `MOVE` 不同)。库要检查 `failedRecords`。
  **[已验证,2026-10-31 库实测]** 这次观察到任务 `status` 是 `FAILED`(顶层 `errCode:0`)。`failedRecords[]` 的元素**没有顶层 `id`**,结构是 `{"errCode":11005,"from":{<源记录>},"to":{<目标里已有的记录>}}`,源 ID 在 `from.id`。
- 复制到文件自己所在的目录:`FAILED`,`errCode:11003`,`errMessage:"Copy forbidden for current directory"`。
- 复制目录到自身或子孙目录:`FAILED`,`errCode:11010`。
- 前端网页没有用到 `COPY`,是直接试出来的。其他拼写(`copy`、`COPY_FILE`、`CLONE`)没有生效,独立的 `/records/copy`、`/files/copy` 路径是 404。

### 7.4 删除的实际效果

- 删除目录:目录和其中的内容一起删除,**回收站里只出现文件,不出现目录**。
- 删除后立即列出父目录,该项已不见(删除、移动、上传的列表是即时一致的)。
- 复制:见 7.3,`operateType=COPY` 存在。

### 7.5 列表一致性 [已验证]

写操作之后立即读列表可能读到旧状态:

| 操作 | 读到旧状态的次数 | 一致所需时间 |
| --- | --- | --- |
| `rename` | 8/8 | 0.26–0.59 秒 |
| `folders/create` | 6/8 | 0.20–0.31 秒 |
| 上传(`commit`)、`MOVE`、`DELETE` | 0/8 | 即时 |

库的做法:`rename` 和 `mkdir` 之后用本地已知的结果更新缓存,不要立刻依赖列表;需要核对时等约 1 秒再读。

---

## 8. 回收站 [已验证]

```
GET  /drive/user/trash?ts=<ms>&cursor=
POST /drive/user/trash/<id>/restore      form: serviceToken=<token>   -> {"code":0,"reason":"success"}
POST /drive/user/trash/<id>/purge        form: serviceToken=<token>   -> {"data":{"deleted":true}}
```

列表响应:`{"data":{"cursor":"<游标>","hasMore":false,"list":[{"id","name","size","type","label","modifyTime"}]}}`,游标分页。**[已验证]** 130 项分 3 页:第一页 10 项,之后每页 100 项,所有 ID 不重复不遗漏。`cursor` 是短字符串,取上一页响应的 `cursor` 作为下一页请求的 `cursor` 参数。
回收站里没有原父目录 ID 和原路径,只能靠记录的文件 ID 自己对应。

| 场景 | 结果 |
| --- | --- |
| 恢复,原父目录还在 | 回到原目录,文件 ID 不变 |
| 恢复,原目录里有同名文件 | 被恢复的文件自动改名为 `name(1).ext`,ID 不变 |
| 恢复,原父目录已被删除 | 服务端**在原路径下重建同名目录**(**新的目录 ID**),文件放进去;同一个被删目录里的其他文件恢复时共用这个重建的目录 |
| 对不在回收站的 ID 做 `restore`/`purge` | 11002 |

没有"清空回收站"的接口,需要逐个 `purge`。

---

## 9. 限流与并发 [已验证]

- 60 个并发列目录请求(16 线程)全部成功,1.4 秒完成。
- 连续 30 次顺序请求,全部成功,约 0.25 秒/次。
- 并发创建 250 个目录(8 线程)全部成功,11.6 秒。
- 没有触发过限流。上限 **[未知]**。

---

## 10. 待办

1. 令牌续期链跨天、休眠后恢复的行为(`passToken` 恢复已验证,见 2.7)。
2. 短信验证码登录:"验证码错误"的返回码(见 2.8 的说明,需要再发一条短信才能测准)、验证码有效期、发送频率限制、图形验证码的触发条件、国际号码格式。
3. 二维码过期(`70031`)和取消(`/longPolling/cancle`)的真实返回。
4. `passToken` 超过 30 天不使用后的失效表现;用户在「登录设备管理」里移除设备后 `passToken` 是否立刻失效。
5. 容量用尽时的错误码(前端常量 `We`)。
6. 端到端加密(E2EE)开启后的上传行为:当前账号 `e2eeStatus` 为 `close`,`encryptInfo` 参数没有测。
7. 大于 20 MiB 文件的上传、断点续传时 `uploadId` 的有效期。
8. 缩略图相关接口(`files/thumbnails`、`create/<id>/thumbnail`)。

## 11. 令牌监控结果 [已验证]

监控脚本跑了约 35 分钟(2026-10-31 15:10–15:45),只记录令牌是否有效,不记录令牌内容。

| 令牌 | 签发时间 | 失效时间 | 寿命 |
| --- | --- | --- | --- |
| T0(浏览器签发,之后不再续期) | 15:06:17 | 15:21:32 前 | 约 15.3 分钟 |
| Ta(由 T0 续期一次得到,之后不再续期) | 15:10:45 | 15:26:07 前 | 约 15.4 分钟 |
| 续期链(每 5 分钟用最新令牌调一次 `/status/lite/setting`) | | 到监控结束仍有效 | 续期 6 次,约 35 分钟 |

结论:

- 令牌寿命约 **15 分钟**,从**签发**时刻算。续期得到的新令牌有独立的 15 分钟,不是延长旧令牌。
- 同一个令牌可以并行续期多次,每次得到不同的新令牌,都有效。
- 每 5 分钟续期一次,链条能一直维持(实测 35 分钟,6 次)。更长时间、跨天、休眠后恢复 **[未测]**。
- 浏览器同时在用的会话和脚本的续期链互不影响。
- 续期间隔建议 5 分钟,留出三倍的余量。如果机器休眠超过 15 分钟,醒来后令牌就已失效,续期会返回 401,此时需要重新登录。
- 令牌断了之后不需要用户介入:账号站的 `passToken` 是长期凭据,配合 `userId` 可以静默换新 `serviceToken`(2.7,已验证)。`passToken` 约 30 天有效且每次使用后顺延,所以库要做的是持久化它。

## 12. 对库设计的影响(汇总)

- 认证:`serviceToken` + `userId` 两个 Cookie;POST 另在表单里带 `serviceToken`。每 5 分钟续期,并把新令牌写回持久化存储。
- 登录方式:Cookie 导入、二维码、短信验证码,三种都已实测走通。不做密码登录。二维码登录无需加密,也不会触发验证码,是最稳的主路径;短信登录只有发验证码一步需要加密。
- 持久化:`passToken` + `userId`(+ `deviceId`)。`passToken` 比 `serviceToken` 敏感得多,用 600 权限文件或系统钥匙串,日志里不得出现。
- 下载:`files/<id>` 取 `storage.downloadUrl`,GET + `Range`,不需要 Cookie。
- 上传:`create` → 并发 PUT 块 → 按顺序 `commit`。必须检查 `stat`。`exists:true` 表示已完成。
- 覆盖写:没有原地更新。用"新文件 + 改名 + 删旧文件"模拟,文件 ID 会变。
- 空文件:不能上传。需要在本地缓存,有内容时才上传。
- 目录列表:`limit` 可以很大,一次读完。写操作后(尤其是 `rename`、`mkdir`)列表有 0.2–0.6 秒的延迟,缓存要以本地为准。
- 同名处理:上传/移动/恢复冲突时服务端自动加 `(1)`;`rename`/`COPY` 冲突时报错。名字大小写不敏感。
- 所有任务型操作(`MOVE`/`COPY`/`DELETE`)要检查 `status`、`errCode` 和 `failedRecords`,不存在的 ID 也会返回 `SUCCESS`。
- 名称校验:ASCII ≤ 200 字符,中文 ≤ 127 字符,并过滤 `/ \ : * ? < > | "`、控制字符、`.`/`..`/`...` 和首尾空格。
- 不要对 `-1` 和 `50005` 自动重试,响应里的 `retriable:true` 不可信。
