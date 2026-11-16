package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// maxTextBody caps how much of a text response we keep in memory. Real eClass
// pages are a few hundred KB, so this only protects against surprises.
const maxTextBody = 20 << 20

// Client talks to one Open eClass instance on behalf of one user.
//
// Login goes through the mobile API (modules/mobile/mlogin.php), the same one
// the official eClass app uses. It hands back a PHP session id which we then
// use as the PHPSESSID cookie for every normal web page. Pages served to a
// "mobile" session skip most of the site chrome, which suits us fine.
type Client struct {
	base       *url.URL
	user, pass string
	http       *http.Client

	mu    sync.Mutex
	token string // current PHP session id, empty until the first login
}

// Field is one name/value pair of a form submission. A list of fields (instead
// of a map) lets callers repeat names such as "recipients[]".
type Field struct {
	Name  string `json:"name" jsonschema:"form field name exactly as shown by eclass_get_page"`
	Value string `json:"value" jsonschema:"value to send"`
}

// NewClient prepares a client for the eClass instance at baseURL. It does not
// touch the network; the first request logs in.
func NewClient(baseURL, user, pass string) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("invalid eClass URL %q", baseURL)
	}
	jar, _ := cookiejar.New(nil) // never fails with nil options
	return &Client{
		base: base,
		user: user,
		pass: pass,
		http: &http.Client{Jar: jar, Timeout: 2 * time.Minute},
	}, nil
}

// login asks the mobile API for a fresh session. When stale is set it only
// logs in if nobody else replaced that exact token in the meantime, so a burst
// of parallel requests hitting an expired session triggers a single login.
func (c *Client) login(ctx context.Context, stale string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.token != stale {
		return nil
	}

	form := url.Values{"uname": {c.user}, "pass": {c.pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.JoinPath("modules/mobile/mlogin.php").String(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("eClass login: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("eClass login: %w", err)
	}

	token := strings.TrimSpace(string(body))
	switch token {
	case "FAILED":
		return errors.New("eClass login failed: wrong username or password (or too many failed attempts)")
	case "USERNOTACTIVE":
		return errors.New("eClass login failed: this account is not active")
	case "NOTENABLED":
		return errors.New("eClass login failed: the mobile API is disabled on this eClass instance")
	}
	if token == "" || strings.ContainsAny(token, " <\n") {
		return fmt.Errorf("eClass login: unexpected answer from server (HTTP %d)", resp.StatusCode)
	}

	c.token = token
	c.http.Jar.SetCookies(c.base, []*http.Cookie{{Name: "PHPSESSID", Value: token, Path: "/"}})
	return nil
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// resolve turns a relative or absolute URL into an absolute one and refuses
// anything outside the eClass host, so the session cookie never leaks.
func (c *Client) resolve(target string) (*url.URL, error) {
	u, err := c.base.Parse(strings.TrimSpace(target))
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", target, err)
	}
	if u.Host != c.base.Host || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("refusing to request %s: only %s is allowed", u, c.base.Host)
	}
	return u, nil
}

// Send performs a request against eClass and returns the response with its
// body still readable. Text bodies are buffered so we can spot an expired
// session; in that case it logs in again and retries once.
//
// method is GET or POST. For GET the fields go into the query string. For POST
// they are sent url-encoded, or as multipart when files (field name to local
// path) are given. ajax marks the request as coming from JavaScript, which is
// what makes eClass list pages answer with JSON instead of HTML.
func (c *Client) Send(ctx context.Context, method, target string, fields, files []Field, ajax bool) (*http.Response, error) {
	u, err := c.resolve(target)
	if err != nil {
		return nil, err
	}
	method = strings.ToUpper(method)
	if method != http.MethodGet && method != http.MethodPost {
		return nil, fmt.Errorf("unsupported method %q, use GET or POST", method)
	}

	for attempt := 0; attempt < 2; attempt++ {
		token := c.currentToken()
		if token == "" || attempt > 0 {
			if err := c.login(ctx, token); err != nil {
				return nil, err
			}
			token = c.currentToken()
		}

		req, err := buildRequest(ctx, method, *u, fields, files, token)
		if err != nil {
			return nil, err
		}
		if ajax {
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if !isText(resp.Header.Get("Content-Type")) {
			return resp, nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxTextBody))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if !sessionExpired(resp.Request.URL, body) {
			return resp, nil
		}
	}
	return nil, errors.New("eClass keeps asking for a login even after logging in again; check the credentials")
}

// buildRequest assembles one attempt of a request. It is rebuilt per attempt
// because a multipart body can only be read once and the token may change.
func buildRequest(ctx context.Context, method string, u url.URL, fields, files []Field, token string) (*http.Request, error) {
	// The mobile endpoints read the session from a POST "token" field instead
	// of the cookie, so hand it to them explicitly.
	if strings.Contains(u.Path, "/modules/mobile/") {
		fields = append([]Field{{Name: "token", Value: token}}, fields...)
	}

	if method == http.MethodGet {
		q := u.Query()
		for _, f := range fields {
			q.Add(f.Name, f.Value)
		}
		u.RawQuery = q.Encode()
		return http.NewRequestWithContext(ctx, method, u.String(), nil)
	}

	if len(files) == 0 {
		form := url.Values{}
		for _, f := range fields {
			form.Add(f.Name, f.Value)
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req, err
	}

	// ponytail: the whole upload is buffered in memory, fine for coursework
	// sized files; switch to an io.Pipe if people start uploading videos.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		if err := mw.WriteField(f.Name, f.Value); err != nil {
			return nil, err
		}
	}
	for _, f := range files {
		if err := attachFile(mw, f.Name, f.Value); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), &buf)
	if err == nil {
		req.Header.Set("Content-Type", mw.FormDataContentType())
	}
	return req, err
}

func attachFile(mw *multipart.Writer, field, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open file for upload: %w", err)
	}
	defer f.Close()
	part, err := mw.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = io.Copy(part, f)
	return err
}

// isText reports whether a response body is worth reading as text (HTML, XML,
// JSON, plain text) instead of streaming it as a file.
func isText(contentType string) bool {
	ct := strings.ToLower(contentType)
	return ct == "" || strings.HasPrefix(ct, "text/") ||
		strings.Contains(ct, "json") || strings.Contains(ct, "xml") || strings.Contains(ct, "javascript")
}

var (
	unameInput = regexp.MustCompile(`name=['"]uname['"]`)
	passInput  = regexp.MustCompile(`name=['"]pass['"]`)
)

// sessionExpired spots the two ways eClass says "you are not logged in": the
// mobile API answers with a bare EXPIRED, the web pages redirect to (or embed)
// the login form.
func sessionExpired(final *url.URL, body []byte) bool {
	if string(bytes.TrimSpace(body)) == "EXPIRED" {
		return true
	}
	if strings.HasSuffix(final.Path, "/login_form.php") {
		return true
	}
	return unameInput.Match(body) && passInput.Match(body)
}
