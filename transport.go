package micloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	hostDrive   = "https://i.mi.com"
	hostAccount = "https://account.xiaomi.com"

	// loginBootURL is the entry point that hands out the account login URL.
	loginBootURL = hostDrive + "/api/user/login?followUp=https%3A%2F%2Fi.mi.com%2F&_locale=zh_CN"

	defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"
)

var startPrefix = []byte("&&&START&&&")

// stripStart removes the account site's "&&&START&&&" JSON guard prefix.
func stripStart(b []byte) []byte {
	return bytes.TrimPrefix(bytes.TrimSpace(b), startPrefix)
}

// apiEnvelope is the common shape of an i.mi.com response. The old-format
// fields (R/S/D) appear only on 401 and other legacy errors.
type apiEnvelope struct {
	Result      string          `json:"result"`
	Retriable   bool            `json:"retriable"`
	Code        int             `json:"code"`
	Reason      string          `json:"reason"`
	Description string          `json:"description"`
	Data        json.RawMessage `json:"data"`

	// Legacy error shape.
	R int    `json:"R"`
	S string `json:"S"`
	D string `json:"D"`
}

// unauthorized reports whether the envelope or status means "log in again".
func (e *apiEnvelope) unauthorized(status int) bool {
	return status == http.StatusUnauthorized || e.R == http.StatusUnauthorized
}

// newHTTPClient builds a client. If follow is false it stops at the first
// redirect and returns that response, so callers can read Set-Cookie headers.
func newHTTPClient(follow bool, timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return c
}

// decodeStartJSON reads an account-site body, strips the guard and unmarshals.
func decodeStartJSON(r io.Reader, v any) error {
	b, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(stripStart(b), v)
}

// setCookieValue returns the value of the named cookie in a response's
// Set-Cookie headers, or "" if absent or cleared.
func setCookieValue(resp *http.Response, name string) string {
	for _, c := range resp.Cookies() {
		if c.Name == name && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// jarCookie returns the named cookie the jar holds for rawURL.
func jarCookie(jar http.CookieJar, rawURL, name string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, c := range jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// encodeForm is url.Values.Encode with a stable nil guard.
func encodeForm(v url.Values) string {
	if v == nil {
		return ""
	}
	return v.Encode()
}

// get performs a plain GET and returns the raw body, used by login helpers that
// do not go through a Session.
func httpGetBody(ctx context.Context, cli *http.Client, rawURL, cookie string, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp, body, err
}

// httpPostForm POSTs a urlencoded form and returns the response and body.
func httpPostForm(ctx context.Context, cli *http.Client, rawURL string, form url.Values, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(encodeForm(form)))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp, body, err
}
