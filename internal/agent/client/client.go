// Package client calls the netdeploy agent API (ADR 0025) from WinPE.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agent/wire"
)

// APIError is a problem response.
type APIError struct {
	Status     int
	Code       string `json:"code"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s (%d %s): %s", e.Title, e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("%s (%d %s)", e.Title, e.Status, e.Code)
}

// Client talks to one server. Calls retry network errors and 5xx with
// back-off; API errors (4xx) return at once.
type Client struct {
	base  *url.URL // e.g. http://10.77.0.1:8080
	http  *http.Client
	token string
	// Retries for transient failures; zero means 5. Sleep is replaceable in
	// tests.
	Retries int
	Sleep   func(time.Duration)
}

// New returns a client for the server at base.
func New(base string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("server URL %q: not an http(s) URL", base)
	}
	return &Client{base: u, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Base is the server URL.
func (c *Client) Base() string { return c.base.String() }

// SetToken sets the bearer token for later calls.
func (c *Client) SetToken(t string) { c.token = t }

// URL resolves a server path (e.g. a plan's /api/v1/agent/files/…).
func (c *Client) URL(p string) string {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	return c.base.String() + "/" + strings.TrimLeft(p, "/")
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (c *Client) retries() int {
	if c.Retries > 0 {
		return c.Retries
	}
	return 5
}

// do sends a JSON request and decodes a JSON response into out (if not nil).
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	var last error
	for attempt := 0; attempt <= c.retries(); attempt++ {
		if attempt > 0 {
			c.sleep(time.Duration(1<<min(attempt-1, 4)) * time.Second)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.URL("/api/v1"+path), bytes.NewReader(body))
		if err != nil {
			return err
		}
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			last = problem(resp, b)
			continue
		}
		if resp.StatusCode >= 400 {
			return problem(resp, b)
		}
		if out != nil && len(b) > 0 {
			return json.Unmarshal(b, out)
		}
		return nil
	}
	return last
}

func problem(resp *http.Response, b []byte) *APIError {
	e := &APIError{Status: resp.StatusCode}
	if json.Unmarshal(b, e) != nil || e.Title == "" {
		e.Title = http.StatusText(resp.StatusCode)
	}
	var secs int
	if _, err := fmt.Sscan(resp.Header.Get("Retry-After"), &secs); err == nil {
		e.RetryAfter = time.Duration(secs) * time.Second
	}
	return e
}

// CheckIn calls POST /agent/check-in.
func (c *Client) CheckIn(ctx context.Context, id wire.Identity) (wire.CheckInResult, error) {
	var out wire.CheckInResult
	return out, c.do(ctx, http.MethodPost, "/agent/check-in", id, &out)
}

// Unlock calls POST /agent/unlock.
func (c *Client) Unlock(ctx context.Context, clientID string, password *string) (wire.UnlockResult, error) {
	var out wire.UnlockResult
	return out, c.do(ctx, http.MethodPost, "/agent/unlock", map[string]any{"clientId": clientID, "password": password}, &out)
}

// StartDeployment calls POST /agent/deployments with a session token.
func (c *Client) StartDeployment(ctx context.Context, sessionToken, profileID string) (wire.Token, error) {
	saved := c.token
	c.token = sessionToken
	defer func() { c.token = saved }()
	var out wire.Token
	return out, c.do(ctx, http.MethodPost, "/agent/deployments", map[string]string{"profileId": profileID}, &out)
}

// Plan calls GET /agent/deployment.
func (c *Client) Plan(ctx context.Context) (wire.Plan, error) {
	var out wire.Plan
	return out, c.do(ctx, http.MethodGet, "/agent/deployment", nil, &out)
}

// Progress calls POST /agent/progress.
func (c *Client) Progress(ctx context.Context, p wire.Progress) error {
	return c.do(ctx, http.MethodPost, "/agent/progress", p, nil)
}

// Logs calls POST /agent/logs.
func (c *Client) Logs(ctx context.Context, entries []wire.LogEntry) error {
	return c.do(ctx, http.MethodPost, "/agent/logs", map[string]any{"entries": entries}, nil)
}

// Complete calls POST /agent/complete.
func (c *Client) Complete(ctx context.Context, succeeded bool, message string) error {
	result := "failed"
	if succeeded {
		result = "succeeded"
	}
	return c.do(ctx, http.MethodPost, "/agent/complete", map[string]string{"result": result, "message": message}, nil)
}

// ErrHashMismatch: a download's SHA-256 isn't the expected one.
var ErrHashMismatch = errors.New("download doesn't match its SHA-256")

// Download fetches f into dest, resuming a partial file with Range, and
// checks its SHA-256. progress (may be nil) gets bytes done and total.
func (c *Client) Download(ctx context.Context, f wire.File, dest string, progress func(done, total int64)) error {
	var last error
	for attempt := 0; attempt <= c.retries(); attempt++ {
		if attempt > 0 {
			c.sleep(time.Duration(1<<min(attempt-1, 4)) * time.Second)
		}
		err := c.downloadOnce(ctx, f, dest, progress)
		if err == nil || ctx.Err() != nil {
			return err
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Status < 500 {
			return err
		}
		if errors.Is(err, ErrHashMismatch) {
			os.Remove(dest) // start over
		}
		last = err
	}
	return last
}

func (c *Client) downloadOnce(ctx context.Context, f wire.File, dest string, progress func(done, total int64)) error {
	out, err := os.OpenFile(dest, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	have, err := out.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if f.SizeBytes > 0 && have > f.SizeBytes {
		if err := out.Truncate(0); err != nil {
			return err
		}
		have, _ = out.Seek(0, io.SeekStart)
	}
	if f.SizeBytes == 0 || have < f.SizeBytes {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(f.ContentURL), nil)
		if err != nil {
			return err
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		resp, err := (&http.Client{}).Do(req) // no overall timeout: files are large
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusPartialContent:
		case http.StatusOK: // no range support or a fresh start
			if err := out.Truncate(0); err != nil {
				return err
			}
			if have, err = out.Seek(0, io.SeekStart); err != nil {
				return err
			}
		default:
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return problem(resp, b)
		}
		total := f.SizeBytes
		if total == 0 {
			total = have + resp.ContentLength
		}
		buf := make([]byte, 1<<20)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, err := out.Write(buf[:n]); err != nil {
					return err
				}
				have += int64(n)
				if progress != nil {
					progress(have, total)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return rerr
			}
		}
	}
	// Verify the whole file.
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, out); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, f.SHA256) {
		return fmt.Errorf("%w: %s is %s, want %s", ErrHashMismatch, dest, got, f.SHA256)
	}
	return nil
}

// Get fetches a URL with the token (e.g. the plan's unattend file).
func (c *Client) Get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(u), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, problem(resp, b)
	}
	return b, nil
}
