package xiaomidrive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Account-site endpoints used by the login flows.
const (
	acctLongPollingLoginURL = hostAccount + "/longPolling/loginUrl"
	acctSendSMSTicket       = hostAccount + "/pass/sendServiceLoginTicket"
	acctPhoneInfo           = hostAccount + "/pass/phoneInfo"
	acctSMSLogin            = hostAccount + "/pass/serviceLoginTicketAuth"
)

// bootParams are the guided-login parameters echoed back in every login flow
// (api-notes §2.4). They carry no secrets.
type bootParams struct {
	Sign         string `json:"_sign"`
	QS           string `json:"qs"`
	Callback     string `json:"callback"`
	ServiceParam string `json:"serviceParam"`
	Sid          string `json:"sid"`
}

func newDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("wb_%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func newFingerprint() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// fetchLoginURL gets the account login URL from the drive host.
func fetchLoginURL(ctx context.Context, cli *http.Client) (string, error) {
	_, body, err := httpGetBody(ctx, cli, loginBootURL, "", nil)
	if err != nil {
		return "", err
	}
	var r struct {
		Data struct {
			LoginURL string `json:"loginUrl"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal(stripStart(body), &r); jerr != nil {
		return "", fmt.Errorf("xiaomidrive: login bootstrap decode: %w", jerr)
	}
	if r.Data.LoginURL == "" {
		return "", fmt.Errorf("xiaomidrive: login bootstrap: empty loginUrl")
	}
	return r.Data.LoginURL, nil
}

// bootstrap fetches the guided-login parameters.
func bootstrap(ctx context.Context, cli *http.Client) (bootParams, error) {
	loginURL, err := fetchLoginURL(ctx, cli)
	if err != nil {
		return bootParams{}, err
	}
	_, body, err := httpGetBody(ctx, cli, loginURL+"&_json=true", "", nil)
	if err != nil {
		return bootParams{}, err
	}
	var p bootParams
	if jerr := json.Unmarshal(stripStart(body), &p); jerr != nil {
		return bootParams{}, fmt.Errorf("xiaomidrive: bootstrap decode: %w", jerr)
	}
	if p.Sid == "" || p.Callback == "" {
		return bootParams{}, fmt.Errorf("xiaomidrive: bootstrap: missing parameters")
	}
	return p, nil
}

// exchangeServiceToken follows the sts location (without auto-redirect) and
// returns the serviceToken and userId it issues via Set-Cookie. It walks up to
// a few redirect hops so a changing redirect chain still yields the cookies.
func exchangeServiceToken(ctx context.Context, cli *http.Client, location, cookie string) (token, userID string, err error) {
	next := location
	for hop := 0; hop < 6 && next != ""; hop++ {
		resp, _, gerr := httpGetBody(ctx, cli, next, cookie, map[string]string{"Referer": hostAccount + "/"})
		if gerr != nil {
			return "", "", gerr
		}
		if t := setCookieValue(resp, "serviceToken"); t != "" {
			token = t
		}
		if id := setCookieValue(resp, "userId"); id != "" {
			userID = id
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			break
		}
		base, perr := url.Parse(next)
		if perr != nil {
			break
		}
		ref, perr := url.Parse(loc)
		if perr != nil {
			break
		}
		next = base.ResolveReference(ref).String()
	}
	return token, userID, nil
}

// acctCookie is the cookie header used for account-site requests.
func acctCookie(deviceID string) string {
	return fmt.Sprintf("deviceId=%s; pass_ua=web; uLocale=zh_CN", deviceID)
}

func acctHeaders(deviceID string, extra map[string]string) map[string]string {
	h := map[string]string{
		"Cookie":           acctCookie(deviceID),
		"X-Requested-With": "XMLHttpRequest",
		"Origin":           hostAccount,
		"Referer":          hostAccount + "/",
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// ---------------------------------------------------------------------------
// Cookie import
// ---------------------------------------------------------------------------

// LoginWithCookies authenticates from user-supplied cookies. At minimum it
// needs serviceToken + userId; adding passToken enables silent long-term
// restore. It verifies the credentials with a lightweight call, falling back to
// a passToken restore if the serviceToken is already expired.
func (c *Client) LoginWithCookies(ctx context.Context, creds Credentials) error {
	if creds.UserID == "" || (creds.ServiceToken == "" && creds.PassToken == "") {
		return fmt.Errorf("%w: need userId plus serviceToken or passToken", ErrInvalidArgument)
	}
	if creds.DeviceID == "" {
		creds.DeviceID = newDeviceID()
	}
	if creds.DeviceFingerprint == "" {
		creds.DeviceFingerprint = newFingerprint()
	}
	c.s.setCredentials(creds)

	if creds.ServiceToken != "" {
		if _, err := c.s.get(ctx, "/status/lite/setting", url.Values{"type": {"AutoRenewal"}, "inactiveTime": {"10"}}); err == nil {
			return nil
		}
	}
	if creds.PassToken != "" {
		return c.s.Refresh(ctx)
	}
	return ErrUnauthorized
}

// ---------------------------------------------------------------------------
// QR login
// ---------------------------------------------------------------------------

// QRChallenge is an in-progress QR login. Render Payload as a QR code for the
// user to scan with the Mi Account or Mi Home app, then call PollQR.
type QRChallenge struct {
	// Payload is the QR code content.
	Payload string
	// Timeout is how many seconds the code stays valid.
	Timeout int

	boot     bootParams
	deviceID string
	fp       string
	lp       string
}

// StartQRLogin requests a QR code. It does not block.
func (c *Client) StartQRLogin(ctx context.Context) (*QRChallenge, error) {
	boot, err := bootstrap(ctx, c.s.noRedir)
	if err != nil {
		return nil, err
	}
	deviceID, fp := newDeviceID(), newFingerprint()
	q := url.Values{
		"_locale": {"zh_CN"}, "_group": {"DEFAULT"}, "sid": {boot.Sid},
		"qs": {boot.QS}, "callback": {boot.Callback}, "_sign": {boot.Sign},
		"serviceParam": {boot.ServiceParam}, "showActiveX": {"false"}, "theme": {""},
		"needTheme": {"false"}, "bizDeviceType": {""}, "_hasLogo": {"false"},
		"_qrsize": {"240"}, "_dc": {strconv.FormatInt(time.Now().UnixMilli(), 10)},
		"deviceFingerprint": {fp},
	}
	_, body, err := httpGetBody(ctx, c.s.noRedir, acctLongPollingLoginURL+"?"+q.Encode(), "", acctHeaders(deviceID, nil))
	if err != nil {
		return nil, err
	}
	var r struct {
		Code     int    `json:"code"`
		LoginURL string `json:"loginUrl"`
		QR       string `json:"qr"`
		LP       string `json:"lp"`
		Timeout  int    `json:"timeout"`
		Desc     string `json:"desc"`
	}
	if jerr := json.Unmarshal(stripStart(body), &r); jerr != nil {
		return nil, fmt.Errorf("xiaomidrive: qr start decode: %w", jerr)
	}
	if r.Code != 0 || r.LoginURL == "" || r.LP == "" {
		return nil, &LoginError{Step: "qr-start", Code: r.Code, Description: r.Desc}
	}
	return &QRChallenge{
		Payload: r.LoginURL, Timeout: r.Timeout,
		boot: boot, deviceID: deviceID, fp: fp, lp: r.LP,
	}, nil
}

// PollQR blocks until the user confirms the login on their phone, the code
// expires (ErrQRExpired), or ctx is cancelled. On success the session is
// authenticated and the credentials are stored.
func (c *Client) PollQR(ctx context.Context, ch *QRChallenge) error {
	// The long poll holds the connection open until the user acts; give it
	// generous headroom over the server's ~300s timeout.
	cli := newHTTPClient(true, 330*time.Second)
	_, body, err := httpGetBody(ctx, cli, ch.lp, "", acctHeaders(ch.deviceID, nil))
	if err != nil {
		return err
	}
	var r struct {
		Code            int        `json:"code"`
		SecurityStatus  int        `json:"securityStatus"`
		UserID          flexString `json:"userId"`
		CUserID         string     `json:"cUserId"`
		PassToken       string     `json:"passToken"`
		Location        string     `json:"location"`
		NotificationURL string     `json:"notificationUrl"`
		Desc            string     `json:"desc"`
	}
	if jerr := json.Unmarshal(stripStart(body), &r); jerr != nil {
		return fmt.Errorf("xiaomidrive: qr poll decode: %w", jerr)
	}
	if r.Code == CodeQRExpired {
		return ErrQRExpired
	}
	if r.Code != 0 || r.SecurityStatus != 0 || r.Location == "" {
		return &LoginError{Step: "qr-confirm", Code: r.Code, Description: r.Desc, NotificationURL: r.NotificationURL}
	}
	return c.finishLogin(ctx, string(r.UserID), r.CUserID, r.PassToken, r.Location, ch.deviceID, ch.fp)
}

// ---------------------------------------------------------------------------
// SMS login
// ---------------------------------------------------------------------------

// SMSChallenge is an in-progress SMS login. After SendSMSCode returns, prompt
// the user for the VCodeLen-digit code (respecting WaitSeconds before any
// resend) and call SMSLogin.
type SMSChallenge struct {
	// VCodeLen is the expected number of digits in the code.
	VCodeLen int
	// WaitSeconds is the cool-down before a code may be resent.
	WaitSeconds int

	boot     bootParams
	deviceID string
	fp       string
	phone    string
}

// SendSMSCode sends one login code to a phone number bound to the account.
// Mainland numbers are the bare 11 digits (no +86).
func (c *Client) SendSMSCode(ctx context.Context, phone string) (*SMSChallenge, error) {
	boot, err := bootstrap(ctx, c.s.noRedir)
	if err != nil {
		return nil, err
	}
	deviceID, fp := newDeviceID(), newFingerprint()
	eui, encUser, err := encryptLoginField("user", phone)
	if err != nil {
		return nil, err
	}
	form := url.Values{"sid": {boot.Sid}, "user": {encUser}, "captCode": {""}}
	rawURL := acctSendSMSTicket + "?_dc=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	_, body, err := httpPostForm(ctx, c.s.noRedir, rawURL, form, acctHeaders(deviceID, map[string]string{"eui": eui}))
	if err != nil {
		return nil, err
	}
	var r struct {
		Code       int    `json:"code"`
		CaptchaURL string `json:"captchaUrl"`
		Desc       string `json:"desc"`
		Data       struct {
			VCodeLen int `json:"vCodeLen"`
			WT       int `json:"wt"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal(stripStart(body), &r); jerr != nil {
		return nil, fmt.Errorf("xiaomidrive: sms send decode: %w", jerr)
	}
	if r.CaptchaURL != "" {
		return nil, &LoginError{Step: "sms-send", Code: r.Code, Description: r.Desc, CaptchaURL: r.CaptchaURL}
	}
	if r.Code != 0 {
		return nil, &LoginError{Step: "sms-send", Code: r.Code, Description: r.Desc}
	}
	return &SMSChallenge{
		VCodeLen: r.Data.VCodeLen, WaitSeconds: r.Data.WT,
		boot: boot, deviceID: deviceID, fp: fp, phone: phone,
	}, nil
}

// SMSLogin verifies the code and logs in. On success the session is
// authenticated and the credentials are stored.
func (c *Client) SMSLogin(ctx context.Context, ch *SMSChallenge, code string) error {
	// Pre-check: confirms the code and that the account exists.
	form := url.Values{"sid": {ch.boot.Sid}, "user": {ch.phone}, "ticket": {code}, "policyName": {"miaccount"}}
	_, body, err := httpPostForm(ctx, c.s.noRedir, acctPhoneInfo, form, acctHeaders(ch.deviceID, nil))
	if err != nil {
		return err
	}
	var pre struct {
		Code int `json:"code"`
		Data struct {
			Status int `json:"status"`
		} `json:"data"`
		Desc string `json:"desc"`
	}
	if jerr := json.Unmarshal(stripStart(body), &pre); jerr != nil {
		return fmt.Errorf("xiaomidrive: phoneInfo decode: %w", jerr)
	}
	if pre.Code != 0 || pre.Data.Status != 2 {
		return &LoginError{Step: "sms-verify", Code: pre.Code, Description: pre.Desc}
	}

	form = url.Values{
		"user": {ch.phone}, "ticket": {code}, "_json": {"true"}, "_locale": {"zh_CN"},
		"callback": {ch.boot.Callback}, "sid": {ch.boot.Sid}, "deviceFingerprint": {ch.fp},
		"qs": {ch.boot.QS}, "_sign": {ch.boot.Sign}, "serviceParam": {ch.boot.ServiceParam},
	}
	_, body, err = httpPostForm(ctx, c.s.noRedir, acctSMSLogin, form, acctHeaders(ch.deviceID, nil))
	if err != nil {
		return err
	}
	var r struct {
		Code            int        `json:"code"`
		SecurityStatus  int        `json:"securityStatus"`
		UserID          flexString `json:"userId"`
		CUserID         string     `json:"cUserId"`
		PassToken       string     `json:"passToken"`
		Location        string     `json:"location"`
		NotificationURL string     `json:"notificationUrl"`
		Desc            string     `json:"desc"`
	}
	if jerr := json.Unmarshal(stripStart(body), &r); jerr != nil {
		return fmt.Errorf("xiaomidrive: sms login decode: %w", jerr)
	}
	if r.Code != 0 || r.SecurityStatus != 0 || r.Location == "" {
		return &LoginError{Step: "sms-login", Code: r.Code, Description: r.Desc, NotificationURL: r.NotificationURL}
	}
	return c.finishLogin(ctx, string(r.UserID), r.CUserID, r.PassToken, r.Location, ch.deviceID, ch.fp)
}

// finishLogin exchanges the sts location for a serviceToken and stores the
// complete credential set.
func (c *Client) finishLogin(ctx context.Context, userID, cUserID, passToken, location, deviceID, fp string) error {
	cookie := ""
	if passToken != "" && userID != "" {
		cookie = fmt.Sprintf("passToken=%s; userId=%s", passToken, userID)
	}
	token, cookieUserID, err := exchangeServiceToken(ctx, c.s.noRedir, location, cookie)
	if err != nil {
		return err
	}
	if token == "" {
		return &LoginError{Step: "sts", Description: "no serviceToken issued"}
	}
	if userID == "" {
		userID = cookieUserID
	}
	c.s.setCredentials(Credentials{
		UserID:            userID,
		PassToken:         passToken,
		ServiceToken:      token,
		DeviceID:          deviceID,
		DeviceFingerprint: fp,
		CUserID:           cUserID,
	})
	return nil
}
