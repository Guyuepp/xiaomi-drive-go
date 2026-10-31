// Package xiaomidrive is an unofficial client library for Xiaomi Cloud Drive
// (小米云盘, i.mi.com).
//
// It covers authentication (cookie import, QR login, SMS-code login, and silent
// passToken restore with background serviceToken keep-alive) and drive
// operations (list, search, get-by-path, download with Range, chunked and
// rapid upload, mkdir, rename, move, copy, delete, and the recycle bin).
//
// The API is reverse-engineered from the web client and may change without
// notice. This project is not affiliated with Xiaomi.
//
// # Credentials and security
//
// A session needs userId plus a serviceToken (short-lived, ~15 min) or a
// passToken (long-lived, ~30 days rolling). The passToken is far more sensitive
// than the serviceToken: persist it only through a Store backed by 0600 storage
// (FileStore does this) and never log it. The library never writes secrets to
// the logger passed via WithLogger.
//
// # Typical use
//
//	store := &xiaomidrive.FileStore{Path: "/path/to/credentials.json"}
//	c := xiaomidrive.NewClient(xiaomidrive.WithStore(store))
//	if !c.LoggedIn() {
//		ch, _ := c.StartQRLogin(ctx)
//		art, _ := xiaomidrive.RenderQR(ch.Payload)
//		fmt.Print(art)
//		_ = c.PollQR(ctx, ch) // blocks until the phone confirms
//	}
//	c.StartKeepAlive(ctx)
//	files, _ := c.List(ctx, xiaomidrive.Root())
//
// The research notes behind the implementation are in the repository's docs/
// directory.
package xiaomidrive
