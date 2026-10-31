// Package micloud is a client library for Xiaomi Cloud Drive (i.mi.com).
//
// It covers authentication (cookie import, QR login, SMS-code login, and silent
// passToken restore with background serviceToken keep-alive) and drive
// operations (list, search, get-by-path, download with Range, chunked and
// rapid upload, mkdir, rename, move, copy, delete, and the recycle bin).
//
// The high-level entry point is Client. Its method set deliberately mirrors an
// OpenList driver so the driver subpackage can forward to it; see that package
// for the mapping.
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
//	store := &micloud.FileStore{Path: "~/.config/micloud/credentials.json"}
//	c := micloud.NewClient(micloud.WithStore(store))
//	if !c.LoggedIn() {
//		ch, _ := c.StartQRLogin(ctx)
//		art, _ := micloud.RenderQR(ch.Payload)
//		fmt.Print(art)
//		_ = c.PollQR(ctx, ch) // blocks until the phone confirms
//	}
//	c.StartKeepAlive(ctx)
//	files, _ := c.List(ctx, micloud.Root())
//
// All knowledge of the undocumented i.mi.com API is recorded in docs/.
package micloud
