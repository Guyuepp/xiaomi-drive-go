// Command micloud is a terminal client for Xiaomi Cloud Drive (i.mi.com),
// built on the github.com/Guyuepp/xiaomi-drive-go library.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Guyuepp/xiaomi-drive-go"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "login":
		err = cmdLogin(ctx, args)
	case "logout":
		err = cmdLogout()
	case "export":
		err = cmdExport()
	case "whoami", "quota":
		err = cmdQuota(ctx)
	case "ls":
		err = cmdLs(ctx, args)
	case "mkdir":
		err = cmdMkdir(ctx, args)
	case "rm":
		err = cmdRm(ctx, args)
	case "mv":
		err = cmdMv(ctx, args)
	case "cp":
		err = cmdCp(ctx, args)
	case "rename":
		err = cmdRename(ctx, args)
	case "get", "download":
		err = cmdGet(ctx, args)
	case "put", "upload":
		err = cmdPut(ctx, args)
	case "link":
		err = cmdLink(ctx, args)
	case "search":
		err = cmdSearch(ctx, args)
	case "trash":
		err = cmdTrash(ctx, args)
	case "gallery", "photos":
		err = cmdGallery(ctx, args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `micloud — Xiaomi Cloud Drive client

用法:
  micloud login qr                        扫码登录
  micloud login sms <手机号>               短信验证码登录
  micloud login cookie --user <id> --service <token> [--pass <passToken>]
                                          用 Cookie 登录(serviceToken 短期,passToken 长期)
  micloud logout                          删除本地保存的凭据
  micloud export                          打印 userId 和 passToken,用于填写 OpenList 存储配置

  micloud whoami                          显示容量使用
  micloud ls [路径]                        列目录(默认根目录)
  micloud mkdir <路径>                     新建目录
  micloud rm <路径>                        删除(移入回收站)
  micloud mv <源路径> <目标目录>            移动
  micloud cp <源路径> <目标目录>            复制(服务端)
  micloud rename <路径> <新名称>           重命名
  micloud get <路径> [-o 输出]             下载
  micloud put <本地文件> <远程目录> [--name 名称]   上传
  micloud link <路径>                      打印直链(24 小时有效)
  micloud search <关键词>                  全盘搜索
  micloud trash ls|restore <id>|purge <id>  回收站

  micloud gallery <子命令>                  云相册:albums、ls、get、put、backup 等,
                                          运行 micloud gallery help 查看

凭据保存在 `+credPath()+`(权限 600)。
`)
}

// ---------------------------------------------------------------------------
// client setup
// ---------------------------------------------------------------------------

func credPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "micloud", "credentials.json")
}

func newClient() *xiaomidrive.Client {
	return xiaomidrive.NewClient(xiaomidrive.WithStore(&xiaomidrive.FileStore{Path: credPath()}))
}

// authedClient returns a logged-in client or an error telling the user to log
// in.
func authedClient() (*xiaomidrive.Client, error) {
	c := newClient()
	if !c.LoggedIn() {
		return nil, errors.New("未登录,请先运行 `micloud login qr`")
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// login
// ---------------------------------------------------------------------------

func cmdLogin(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("用法: micloud login qr|sms|cookie")
	}
	c := newClient()
	switch args[0] {
	case "qr":
		return loginQR(ctx, c)
	case "sms":
		if len(args) < 2 {
			return errors.New("用法: micloud login sms <手机号>")
		}
		return loginSMS(ctx, c, args[1])
	case "cookie":
		return loginCookie(ctx, c, args[1:])
	default:
		return fmt.Errorf("未知登录方式 %q", args[0])
	}
}

func loginQR(ctx context.Context, c *xiaomidrive.Client) error {
	ch, err := c.StartQRLogin(ctx)
	if err != nil {
		return err
	}
	art, err := xiaomidrive.RenderQR(ch.Payload)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Print(art)
	fmt.Printf("\n请用「小米账号」App 或米家 App 扫描上面的二维码并确认登录(%d 秒内有效)。\n等待确认中…\n", ch.Timeout)
	if err := c.PollQR(ctx, ch); err != nil {
		if errors.Is(err, xiaomidrive.ErrQRExpired) {
			return errors.New("二维码已过期,请重试")
		}
		return err
	}
	fmt.Println("登录成功。")
	return nil
}

func loginSMS(ctx context.Context, c *xiaomidrive.Client, phone string) error {
	ch, err := c.SendSMSCode(ctx, phone)
	if err != nil {
		return err
	}
	fmt.Printf("已发送 %d 位验证码(约 %d 秒后可重发)。\n", ch.VCodeLen, ch.WaitSeconds)
	fmt.Print("请输入验证码: ")
	code, err := readLine()
	if err != nil {
		return err
	}
	if err := c.SMSLogin(ctx, ch, strings.TrimSpace(code)); err != nil {
		return err
	}
	fmt.Println("登录成功。")
	return nil
}

func loginCookie(ctx context.Context, c *xiaomidrive.Client, args []string) error {
	var creds xiaomidrive.Credentials
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--user":
			i++
			creds.UserID = arg(args, i)
		case "--service":
			i++
			creds.ServiceToken = arg(args, i)
		case "--pass":
			i++
			creds.PassToken = arg(args, i)
		default:
			return fmt.Errorf("未知参数 %q", args[i])
		}
	}
	if err := c.LoginWithCookies(ctx, creds); err != nil {
		return err
	}
	fmt.Println("登录成功。")
	return nil
}

func cmdExport() error {
	creds := newClient().Session().Credentials()
	if creds.UserID == "" || creds.PassToken == "" {
		return errors.New("没有可导出的 passToken,请先运行 `micloud login qr` 或 `micloud login sms`")
	}
	fmt.Fprintln(os.Stderr, "注意:passToken 等同于长期登录凭据,不要发给别人,也不要贴到聊天、截图或日志里。")
	fmt.Printf("user_id:    %s\npass_token: %s\n", creds.UserID, creds.PassToken)
	return nil
}

func cmdLogout() error {
	err := os.Remove(credPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("已删除本地凭据。")
	return nil
}

// ---------------------------------------------------------------------------
// read-only commands
// ---------------------------------------------------------------------------

func cmdQuota(ctx context.Context) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	q, err := c.Quota(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("已用 %s / %s(剩余 %s)\n", humanBytes(q.Used), humanBytes(q.Total), humanBytes(q.Free()))
	if len(q.UsedDetail) > 0 {
		fmt.Println("分项:")
		for k, v := range q.UsedDetail {
			fmt.Printf("  %-16s %s\n", k, humanBytes(v))
		}
	}
	return nil
}

func cmdLs(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	p := "/"
	if len(args) > 0 {
		p = args[0]
	}
	dir, err := c.GetByPath(ctx, p)
	if err != nil {
		return err
	}
	if !dir.IsDir() {
		printFile(dir)
		return nil
	}
	files, err := c.List(ctx, dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		printFile(f)
	}
	return nil
}

func printFile(f *xiaomidrive.File) {
	kind := "-"
	size := humanBytes(f.GetSize())
	if f.IsDir() {
		kind = "d"
		size = "-"
	}
	fmt.Printf("%s %10s  %s  %s\n", kind, size, f.ModTime().Format("2006-01-02 15:04"), f.GetName())
}

func cmdLink(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return errors.New("用法: micloud link <路径>")
	}
	f, err := c.GetByPath(ctx, args[0])
	if err != nil {
		return err
	}
	if f.IsDir() {
		return xiaomidrive.ErrIsDir
	}
	l, err := c.Link(ctx, f.GetID())
	if err != nil {
		return err
	}
	fmt.Println(l.URL)
	return nil
}

func cmdSearch(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return errors.New("用法: micloud search <关键词>")
	}
	files, err := c.Search(ctx, args[0], xiaomidrive.SearchOptions{})
	if err != nil {
		return err
	}
	for _, f := range files {
		kind := "file"
		if f.IsDir() {
			kind = "dir "
		}
		fmt.Printf("%s  %-12s  %s\n", kind, f.GetID(), f.GetName())
	}
	return nil
}

// ---------------------------------------------------------------------------
// write commands
// ---------------------------------------------------------------------------

func cmdMkdir(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return errors.New("用法: micloud mkdir <路径>")
	}
	parentPath, name := path.Split(strings.TrimRight(cleanPath(args[0]), "/"))
	parent, err := c.GetByPath(ctx, parentPath)
	if err != nil {
		return err
	}
	f, err := c.MakeDir(ctx, parent.GetID(), name)
	if err != nil {
		return err
	}
	fmt.Printf("已创建目录 %s(id %s)\n", args[0], f.GetID())
	return nil
}

func cmdRm(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return errors.New("用法: micloud rm <路径>")
	}
	f, err := c.GetByPath(ctx, args[0])
	if err != nil {
		return err
	}
	if f.IsRoot() {
		return errors.New("不能删除根目录")
	}
	if err := c.Remove(ctx, []*xiaomidrive.File{f}); err != nil {
		return err
	}
	fmt.Printf("已删除 %s(移入回收站)\n", args[0])
	return nil
}

func cmdMv(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return errors.New("用法: micloud mv <源路径> <目标目录>")
	}
	src, err := c.GetByPath(ctx, args[0])
	if err != nil {
		return err
	}
	dst, err := c.GetByPath(ctx, args[1])
	if err != nil {
		return err
	}
	if !dst.IsDir() {
		return xiaomidrive.ErrNotDir
	}
	if err := c.Move(ctx, []*xiaomidrive.File{src}, dst.GetID()); err != nil {
		return err
	}
	fmt.Printf("已移动 %s -> %s\n", args[0], args[1])
	return nil
}

func cmdCp(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return errors.New("用法: micloud cp <源路径> <目标目录>")
	}
	src, err := c.GetByPath(ctx, args[0])
	if err != nil {
		return err
	}
	dst, err := c.GetByPath(ctx, args[1])
	if err != nil {
		return err
	}
	if !dst.IsDir() {
		return xiaomidrive.ErrNotDir
	}
	if err := c.Copy(ctx, []*xiaomidrive.File{src}, dst.GetID()); err != nil {
		return err
	}
	fmt.Printf("已复制 %s -> %s\n", args[0], args[1])
	return nil
}

func cmdRename(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return errors.New("用法: micloud rename <路径> <新名称>")
	}
	f, err := c.GetByPath(ctx, args[0])
	if err != nil {
		return err
	}
	if err := c.Rename(ctx, f.GetID(), args[1]); err != nil {
		return err
	}
	fmt.Printf("已重命名为 %s\n", args[1])
	return nil
}

func cmdGet(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	var remote, out string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			i++
			out = arg(args, i)
		default:
			if remote == "" {
				remote = args[i]
			}
		}
	}
	if remote == "" {
		return errors.New("用法: micloud get <路径> [-o 输出]")
	}
	f, err := c.GetByPath(ctx, remote)
	if err != nil {
		return err
	}
	if f.IsDir() {
		return xiaomidrive.ErrIsDir
	}
	if out == "" {
		out = f.GetName()
	}
	rc, err := c.Open(ctx, f.GetID())
	if err != nil {
		return err
	}
	defer rc.Close()
	dst, err := os.Create(out)
	if err != nil {
		return err
	}
	defer dst.Close()
	n, err := io.Copy(dst, rc)
	if err != nil {
		return err
	}
	fmt.Printf("已下载 %s(%s)-> %s\n", remote, humanBytes(n), out)
	return nil
}

func cmdPut(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	var local, remoteDir, name string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			name = arg(args, i)
		default:
			if local == "" {
				local = args[i]
			} else if remoteDir == "" {
				remoteDir = args[i]
			}
		}
	}
	if local == "" || remoteDir == "" {
		return errors.New("用法: micloud put <本地文件> <远程目录> [--name 名称]")
	}
	fi, err := os.Stat(local)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return errors.New("暂不支持上传目录")
	}
	dir, err := c.GetByPath(ctx, remoteDir)
	if err != nil {
		return err
	}
	if !dir.IsDir() {
		return xiaomidrive.ErrNotDir
	}
	if name == "" {
		name = filepath.Base(local)
	}
	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer src.Close()

	size := fi.Size()
	opts := xiaomidrive.UploadOptions{ModTime: fi.ModTime()}
	printed := false
	if size > 0 {
		last := -1
		opts.Progress = func(uploaded int64) {
			pct := int(float64(uploaded) / float64(size) * 100)
			if pct != last {
				last = pct
				printed = true
				fmt.Fprintf(os.Stderr, "\r上传中… %d%%", pct)
			}
		}
	}
	f, err := c.UploadReaderAt(ctx, dir.GetID(), name, src, size, opts)
	if printed {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		if errors.Is(err, xiaomidrive.ErrEmptyFile) {
			return errors.New("小米云盘不接受空文件")
		}
		return err
	}
	fmt.Printf("已上传 %s -> %s/%s(id %s)\n", local, strings.TrimRight(remoteDir, "/"), f.GetName(), f.GetID())
	return nil
}

func cmdTrash(ctx context.Context, args []string) error {
	c, err := authedClient()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("用法: micloud trash ls|restore <id>|purge <id>")
	}
	switch args[0] {
	case "ls", "list":
		items, err := c.TrashList(ctx)
		if err != nil {
			return err
		}
		for _, it := range items {
			fmt.Printf("%-12s  %10s  %s  %s\n", it.ID, humanBytes(it.Size), it.Modified.Format("2006-01-02 15:04"), it.Name)
		}
		return nil
	case "restore":
		if len(args) < 2 {
			return errors.New("用法: micloud trash restore <id>")
		}
		if err := c.Restore(ctx, args[1]); err != nil {
			return err
		}
		fmt.Println("已恢复。")
		return nil
	case "purge":
		if len(args) < 2 {
			return errors.New("用法: micloud trash purge <id>")
		}
		if err := c.Purge(ctx, args[1]); err != nil {
			return err
		}
		fmt.Println("已永久删除。")
		return nil
	default:
		return fmt.Errorf("未知子命令 %q", args[0])
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func cleanPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func readLine() (string, error) {
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
