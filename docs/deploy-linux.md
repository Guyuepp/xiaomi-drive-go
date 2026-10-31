# Linux 部署示例:OpenList + rclone 挂载

把小米云盘挂到 `~/MiCloud`。OpenList 作为 systemd 用户服务运行,只监听本机;rclone 通过 OpenList 的 WebDAV 挂载。

## 1. 登录并导出凭据

```
micloud login qr
micloud export        # 记下 user_id 和 pass_token,不要外传
```

## 2. 安装 OpenList(含 MiCloud 驱动)

从 [OpenList fork 的 Releases](https://github.com/Guyuepp/OpenList/releases) 下载,放到 `~/.local/bin/openlist`。

也可以从源码构建:把 fork 克隆到本仓库的 `openlist/` 目录,然后运行 `scripts/build-openlist.sh`。它会下载匹配版本的前端,编译并安装到 `~/.local/bin/openlist`。

## 3. 初始化

```
mkdir -p ~/.local/share/openlist && chmod 700 ~/.local/share/openlist
OPENLIST_ADMIN_PASSWORD='<自己设一个强密码>' openlist admin --data ~/.local/share/openlist
```

新版 OpenList 默认等待网页安装向导创建管理员;设置 `OPENLIST_ADMIN_PASSWORD` 会直接创建。

把 `~/.local/share/openlist/config.json` 里的 `scheme.address` 改为 `127.0.0.1`,只允许本机访问。

## 4. systemd 服务

把 [`deploy/systemd/`](../deploy/systemd/) 下的两个文件复制到 `~/.config/systemd/user/`:

```
systemctl --user daemon-reload
systemctl --user enable --now openlist
```

用户服务只在登录期间运行。需要开机后不登录也运行时,执行 `sudo loginctl enable-linger $USER`。

## 5. 在 OpenList 里配置

打开 `http://127.0.0.1:5244`,用 admin 登录:

1. **添加存储**:驱动 `MiCloud`,挂载路径 `/micloud`,填入 `user_id` 和 `pass_token`。
2. **显示容量**(可选):设置里关闭 `hide_storage_details`。
3. **给 rclone 建一个专用用户**,不要用 admin:
   - 基础路径 `/micloud`。
   - 权限:上传/新建目录、重命名、移动、复制、删除、WebDAV 读取、WebDAV 管理。
   - OpenList 的 WebDAV 写操作除了要"WebDAV 管理",还要对应的普通权限,只开"WebDAV 管理"会全部返回 403。

## 6. rclone 挂载

```
rclone config create micloud webdav url http://127.0.0.1:5244/dav/ vendor other user rclone pass '<rclone 用户的密码>'
chmod 600 ~/.config/rclone/rclone.conf
systemctl --user enable --now rclone-micloud
```

`rclone config create` 会自动混淆密码。混淆不是加密,配置文件要保持 600 权限。

挂载服务使用 `--vfs-cache-mode full`:写入先进本地缓存 `~/.cache/rclone`,文件关闭后在后台上传。缓存上限 10G,保留 24 小时。

## 日常维护

```
systemctl --user status openlist rclone-micloud
journalctl --user -u openlist -f
journalctl --user -u rclone-micloud -f
```

## 注意事项

- 用手机或网页上传的新文件,不会立刻出现在挂载点里:rclone 的目录缓存约 1 分钟,OpenList 的目录缓存时长在存储设置里调(添加存储时填的"缓存过期时间")。
- 通过 WebDAV 覆盖文件时,OpenList 会先在服务端备份一份,成功后删除,所以回收站里会多出文件。
- 空文件作为本地占位记录在存储配置里。在 OpenList 网页里编辑这个存储并保存后,这些记录是否保留**没有验证过**。

## macOS

程序可以编译成 macOS 版本,但**没有在 Mac 上实测过**。建议用 `rclone nfsmount`(使用系统自带的 NFS 客户端,不需要 macFUSE),或 `rclone mount` 配合 macFUSE / FUSE-T。直接用 Finder「连接服务器」挂 WebDAV 时,建议在 OpenList 设置里打开 `ignore_system_files`,过滤 `.DS_Store` 和 `._*` 文件。
