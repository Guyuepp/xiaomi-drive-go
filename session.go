package xiaomidrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Credentials are everything needed to authenticate and to silently restore a
// session. PassToken is long-lived (~30 days, rolling) and far more sensitive
// than ServiceToken (~15 min); persist it only with 0600 permissions and never
// log it.
type Credentials struct {
	UserID            string `json:"userId"`
	PassToken         string `json:"passToken,omitempty"`
	ServiceToken      string `json:"serviceToken,omitempty"`
	DeviceID          string `json:"deviceId,omitempty"`
	DeviceFingerprint string `json:"deviceFingerprint,omitempty"`
	// CUserID is informational; the service does not require it.
	CUserID string `json:"cUserId,omitempty"`
}

func (c Credentials) canAuthenticate() bool {
	return c.UserID != "" && (c.ServiceToken != "" || c.PassToken != "")
}

func (c Credentials) canRestore() bool {
	return c.UserID != "" && c.PassToken != ""
}

// Store persists credentials between runs.
type Store interface {
	Load() (*Credentials, error)
	Save(*Credentials) error
}

// FileStore keeps credentials in a 0600 JSON file.
type FileStore struct {
	Path string
}

// Load reads the file. A missing file yields empty credentials and no error.
func (s *FileStore) Load() (*Credentials, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Credentials{}, nil
		}
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the file atomically with 0600 permissions.
func (s *FileStore) Save(c *Credentials) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(s.Path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.Path)
}

// memStore is an in-memory Store, the default when none is supplied.
type memStore struct {
	mu sync.Mutex
	c  Credentials
}

func (m *memStore) Load() (*Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.c
	return &c, nil
}

func (m *memStore) Save(c *Credentials) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c = *c
	return nil
}

// Session owns the credentials and the HTTP machinery. It is safe for
// concurrent use. Business calls recover from an expired serviceToken
// automatically by restoring from passToken.
type Session struct {
	mu    sync.RWMutex
	creds Credentials
	// persistMu orders credential changes and their snapshots without holding
	// mu while calling a user-supplied Store.
	persistMu sync.Mutex

	store   Store
	http    *http.Client // follows redirects (downloads, storage hosts)
	noRedir *http.Client // stops at first redirect (sts exchange)

	logf                func(string, ...any)
	galleryReadInterval time.Duration
	taskTimeout         time.Duration

	// refreshMu serialises passToken restores so concurrent 401s trigger a
	// single exchange.
	refreshMu sync.Mutex

	kaOnce   sync.Once
	stopOnce sync.Once
	kaStop   chan struct{}
}

// Option configures a Session / Client.
type Option func(*Session)

// WithStore sets the credential store.
func WithStore(s Store) Option { return func(se *Session) { se.store = s } }

// WithHTTPTimeout sets the timeout of the general-purpose client. The default
// (0) means no timeout, which suits large downloads; per-call deadlines come
// from the context.
func WithHTTPTimeout(d time.Duration) Option {
	return func(se *Session) { se.http = newHTTPClient(true, d) }
}

// WithLogger installs a printf-style logger. It must never be passed secrets;
// the library only logs endpoints and non-sensitive status.
func WithLogger(f func(string, ...any)) Option {
	return func(se *Session) { se.logf = f }
}

// WithGalleryReadInterval sets the minimum spacing between gallery GETs for
// this client. The default is 3 seconds; zero disables proactive spacing.
// The cooldown after ErrBlocked remains enabled.
func WithGalleryReadInterval(d time.Duration) Option {
	return func(s *Session) {
		if d >= 0 {
			s.galleryReadInterval = d
		}
	}
}

// WithTaskTimeout sets the deadline for waiting on server-side file operations.
// The default is two minutes; zero uses only the caller's context deadline.
func WithTaskTimeout(d time.Duration) Option {
	return func(s *Session) {
		if d >= 0 {
			s.taskTimeout = d
		}
	}
}

func newSession(opts ...Option) *Session {
	s := &Session{
		store:               &memStore{},
		http:                newHTTPClient(true, 0),
		noRedir:             newHTTPClient(false, 60*time.Second),
		logf:                func(string, ...any) {},
		kaStop:              make(chan struct{}),
		galleryReadInterval: 3 * time.Second,
		taskTimeout:         2 * time.Minute,
	}
	for _, o := range opts {
		o(s)
	}
	if c, err := s.store.Load(); err != nil {
		s.logf("xiaomidrive: load credentials: %v", err)
	} else if c != nil {
		s.creds = *c
	}
	return s
}

// Credentials returns a copy of the current credentials.
func (s *Session) Credentials() Credentials {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds
}

// LoggedIn reports whether the session has enough to authenticate.
func (s *Session) LoggedIn() bool {
	return s.Credentials().canAuthenticate()
}

// setCredentials replaces the credentials and persists them.
func (s *Session) setCredentials(c Credentials) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if err := s.store.Save(&c); err != nil {
		return fmt.Errorf("xiaomidrive: save credentials: %w", err)
	}
	s.mu.Lock()
	s.creds = c
	s.mu.Unlock()
	return nil
}

// updateServiceToken records a freshly issued serviceToken (and userId) and
// persists it.
func (s *Session) updateServiceToken(token, userID string) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	if token != "" {
		s.creds.ServiceToken = token
	}
	if userID != "" {
		s.creds.UserID = userID
	}
	snapshot := s.creds
	s.mu.Unlock()
	if err := s.store.Save(&snapshot); err != nil {
		s.logf("xiaomidrive: save credentials: %v", err)
	}
}

func (s *Session) serviceToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds.ServiceToken
}

func (s *Session) userID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds.UserID
}

// get issues an authenticated GET and returns the decoded data payload.
func (s *Session) get(ctx context.Context, path string, query url.Values) (json.RawMessage, error) {
	return s.call(ctx, http.MethodGet, path, query, nil)
}

// postForm issues an authenticated POST. The serviceToken form field the
// service requires is added per attempt.
func (s *Session) postForm(ctx context.Context, path string, form url.Values) (json.RawMessage, error) {
	if form == nil {
		form = url.Values{}
	}
	return s.call(ctx, http.MethodPost, path, nil, form)
}

// maxGetAttempts bounds retries of idempotent GETs on transport errors.
const maxGetAttempts = 3

// call performs an authenticated request and recovers once from a 401 by
// restoring the session from passToken.
func (s *Session) call(ctx context.Context, method, path string, query, form url.Values) (json.RawMessage, error) {
	if !s.LoggedIn() {
		return nil, ErrUnauthorized
	}
	for attempt := 0; attempt < 2; attempt++ {
		token := s.serviceToken()
		env, status, err := s.callWithRetry(ctx, method, path, query, form, token)
		if err != nil {
			return nil, err
		}
		if env.unauthorized(status) {
			if attempt == 0 {
				if rerr := s.refreshIfStale(ctx, token); rerr != nil {
					return nil, rerr
				}
				continue
			}
			return nil, &APIError{Endpoint: path, HTTPStatus: http.StatusUnauthorized}
		}
		if env.Code != 0 {
			return env.Data, &APIError{
				Endpoint:    path,
				HTTPStatus:  status,
				Code:        env.Code,
				Reason:      env.Reason,
				Description: env.Description,
			}
		}
		return env.Data, nil
	}
	return nil, ErrUnauthorized
}

// callWithRetry retries GETs on transport errors with a short backoff. POSTs
// are never retried: they are not idempotent (e.g. filemanager tasks).
func (s *Session) callWithRetry(ctx context.Context, method, path string, query, form url.Values, token string) (*apiEnvelope, int, error) {
	attempts := 1
	if method == http.MethodGet {
		attempts = maxGetAttempts
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := sleepCtx(ctx, time.Duration(i)*500*time.Millisecond); err != nil {
				return nil, 0, err
			}
		}
		env, status, err := s.callOnce(ctx, method, path, query, form, token)
		if err == nil {
			return env, status, nil
		}
		if status != 0 {
			return nil, status, err
		}
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *Session) callOnce(ctx context.Context, method, path string, query, form url.Values, token string) (*apiEnvelope, int, error) {
	rawURL := hostDrive + path
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}
	var resp *http.Response
	var body []byte
	var err error
	cookie := fmt.Sprintf("serviceToken=%s; userId=%s", token, s.userID())
	if method == http.MethodPost {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		f.Set("serviceToken", token)
		resp, body, err = httpPostForm(ctx, s.http, rawURL, f, map[string]string{"Cookie": cookie})
	} else {
		resp, body, err = httpGetBody(ctx, s.http, rawURL, cookie, nil)
	}
	if err != nil {
		return nil, 0, err
	}
	var decoded struct {
		apiEnvelope
		Code *int `json:"code"`
	}
	jerr := json.Unmarshal(stripStart(body), &decoded)
	env := decoded.apiEnvelope
	if decoded.Code != nil {
		env.Code = *decoded.Code
	}
	// A 401 may carry HTML or no body. Keep the status available to call so
	// it can restore from passToken instead of failing during JSON decoding.
	if resp.StatusCode == http.StatusUnauthorized || (jerr == nil && env.unauthorized(resp.StatusCode)) {
		return &env, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		if jerr != nil {
			env = apiEnvelope{}
		}
		return nil, resp.StatusCode, &APIError{Endpoint: path, HTTPStatus: resp.StatusCode,
			Code: env.Code, Reason: env.Reason, Description: env.Description}
	}
	if jerr != nil {
		return nil, resp.StatusCode, fmt.Errorf("xiaomidrive: %s: decode response: %w", path, jerr)
	}
	if decoded.Code == nil {
		return nil, resp.StatusCode, fmt.Errorf("xiaomidrive: %s: response missing code", path)
	}
	// Accept replacement credentials only from a validated successful response.
	if env.Code == 0 {
		if tok := setCookieValue(resp, "serviceToken"); tok != "" {
			s.updateServiceToken(tok, setCookieValue(resp, "userId"))
		}
	}
	return &env, resp.StatusCode, nil
}

// refreshIfStale restores the session unless another goroutine already
// replaced the token that just failed.
func (s *Session) refreshIfStale(ctx context.Context, failedToken string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.serviceToken() != failedToken {
		return nil
	}
	return s.refreshLocked(ctx)
}

// Refresh exchanges the passToken for a new serviceToken (api-notes §2.7). It
// is called automatically on 401 but may also be invoked directly.
func (s *Session) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	return s.refreshLocked(ctx)
}

func (s *Session) refreshLocked(ctx context.Context) error {
	creds := s.Credentials()
	if !creds.canRestore() {
		return ErrUnauthorized
	}
	loginURL, err := fetchLoginURL(ctx, s.noRedir)
	if err != nil {
		return err
	}
	cookie := fmt.Sprintf("passToken=%s; userId=%s", creds.PassToken, creds.UserID)
	if creds.DeviceID != "" {
		cookie += "; deviceId=" + creds.DeviceID + "; pass_ua=web"
	}
	_, body, err := httpGetBody(ctx, s.noRedir, loginURL+"&_json=true", cookie, nil)
	if err != nil {
		return err
	}
	var r struct {
		Code           int    `json:"code"`
		SecurityStatus int    `json:"securityStatus"`
		Location       string `json:"location"`
		Desc           string `json:"desc"`
	}
	if jerr := json.Unmarshal(stripStart(body), &r); jerr != nil {
		return fmt.Errorf("xiaomidrive: restore: decode: %w", jerr)
	}
	// Success requires code==0 AND securityStatus==0; a failure response also
	// carries a location (to the login page), so location alone is not enough.
	if r.Code != 0 || r.SecurityStatus != 0 || r.Location == "" {
		return &LoginError{Step: "restore", Code: r.Code, Description: r.Desc}
	}
	token, userID, err := exchangeServiceToken(ctx, s.noRedir, r.Location, cookie)
	if err != nil {
		return err
	}
	if token == "" {
		return &LoginError{Step: "restore", Code: r.Code, Description: "no serviceToken issued"}
	}
	if userID == "" {
		userID = creds.UserID
	}
	s.updateServiceToken(token, userID)
	return nil
}

// StartKeepAlive launches a goroutine that renews the serviceToken every 5
// minutes (api-notes §3). It is a no-op if already started. Stop it with Stop.
func (s *Session) StartKeepAlive(ctx context.Context) {
	s.kaOnce.Do(func() {
		go func() {
			t := time.NewTicker(5 * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-s.kaStop:
					return
				case <-t.C:
					if err := s.renew(ctx); err != nil {
						s.logf("xiaomidrive: keepalive: %v", err)
					}
				}
			}
		}()
	})
}

// Stop halts the keep-alive goroutine. It is safe to call more than once.
func (s *Session) Stop() {
	s.stopOnce.Do(func() { close(s.kaStop) })
}

// renew hits a status endpoint; the new serviceToken arrives via Set-Cookie and
// is absorbed in callOnce.
func (s *Session) renew(ctx context.Context) error {
	q := url.Values{"type": {"AutoRenewal"}, "inactiveTime": {"10"}}
	_, err := s.get(ctx, "/status/lite/setting", q)
	return err
}
