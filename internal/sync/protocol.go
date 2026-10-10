// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package sync

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The wire protocol (docs/sync-protocol.md). Keep the two in step.
const (
	MaxBatchEvents = 2000
	MaxBatchBytes  = 8 << 20 // uncompressed request body
)

// Client speaks the sync protocol to one endpoint.
type Client struct {
	Endpoint string // base URL, no trailing slash
	HTTP     *http.Client
	// UserAgent identifies the client version, e.g. "shiplino/0.3.0".
	UserAgent string
}

// NewClient returns a client with sensible timeouts.
func NewClient(endpoint, version string) *Client {
	return &Client{Endpoint: strings.TrimRight(endpoint, "/"), HTTP: &http.Client{Timeout: 60 * time.Second}, UserAgent: "shiplino/" + version}
}

// DeviceCode starts an RFC 8628 device authorization.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`   // seconds between polls
	ExpiresIn               int    `json:"expires_in"` // seconds
}

// Token is a successful sign-in or refresh.
type Token struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresIn     int    `json:"expires_in"` // seconds
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
}

// Device flow outcomes other than success.
var (
	ErrPending  = errors.New("authorization pending")
	ErrSlowDown = errors.New("slow down")
	ErrExpired  = errors.New("the code expired before it was approved")
	ErrDenied   = errors.New("sign-in was denied")
)

// StatusError is a non-2xx answer from the service.
type StatusError struct {
	Code       int
	Message    string        // the service's "error" field, if any
	RetryAfter time.Duration // from Retry-After, if any
}

func (e *StatusError) Error() string {
	msg := http.StatusText(e.Code)
	if e.Message != "" {
		msg = e.Message
	}
	return fmt.Sprintf("sync service: %d %s", e.Code, msg)
}

// IsStatus reports whether err is a StatusError with one of the codes.
func IsStatus(err error, codes ...int) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	for _, c := range codes {
		if se.Code == c {
			return true
		}
	}
	return false
}

// StartDevice asks for a device code: POST /v1/device/code. The device
// name (the hostname) is shown on the approval page.
func (c *Client) StartDevice(ctx context.Context, deviceName string) (DeviceCode, error) {
	var d DeviceCode
	err := c.do(ctx, "POST", "/v1/device/code", "", map[string]string{"device_name": deviceName}, &d)
	if err == nil && (d.DeviceCode == "" || d.UserCode == "" || d.VerificationURI == "") {
		err = errors.New("sync service: incomplete device code response")
	}
	return d, err
}

// PollDevice asks once whether the user approved the code:
// POST /v1/device/token. It returns ErrPending, ErrSlowDown (joined with
// the *StatusError, which may carry Retry-After), ErrExpired or ErrDenied
// until it returns a token.
func (c *Client) PollDevice(ctx context.Context, deviceCode string) (*Token, error) {
	var t Token
	err := c.do(ctx, "POST", "/v1/device/token", "", map[string]string{"device_code": deviceCode}, &t)
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Message {
		case "authorization_pending":
			return nil, ErrPending
		case "slow_down":
			return nil, errors.Join(ErrSlowDown, se)
		case "expired_token":
			return nil, ErrExpired
		case "access_denied":
			return nil, ErrDenied
		}
	}
	if err != nil {
		return nil, err
	}
	return &t, checkToken(&t)
}

// Refresh trades a refresh token for new tokens: POST /v1/token/refresh.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	var t Token
	if err := c.do(ctx, "POST", "/v1/token/refresh", "", map[string]string{"refresh_token": refreshToken}, &t); err != nil {
		return nil, err
	}
	return &t, checkToken(&t)
}

func checkToken(t *Token) error {
	if t.AccessToken == "" || t.WorkspaceID == "" {
		return errors.New("sync service: incomplete token response")
	}
	return nil
}

// Batch is the body of POST /v1/sync/events.
type Batch struct {
	DeviceID   string            `json:"device_id"`
	DeviceName string            `json:"device_name"`
	Events     []json.RawMessage `json:"events"`
}

// Result is the answer to an upload.
type Result struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	// Rejected events failed the service's schema check and were
	// dropped; resending them can't help.
	Rejected int `json:"rejected"`
}

// Upload sends one gzip-compressed batch: POST /v1/sync/events.
func (c *Client) Upload(ctx context.Context, accessToken string, b Batch) (Result, error) {
	var r Result
	err := c.do(ctx, "POST", "/v1/sync/events", accessToken, b, &r)
	return r, err
}

// Me returns what the service knows about this device's sign-in:
// GET /v1/me, {"user": {…}, "workspace": {…, "role"}, "device": {…}}.
// Fields are passed through as the service sends them.
func (c *Client) Me(ctx context.Context, accessToken string) (map[string]any, error) {
	var m map[string]any
	err := c.do(ctx, "GET", "/v1/me", accessToken, nil, &m)
	return m, err
}

func (c *Client) do(ctx context.Context, method, path, token string, body, out any) error {
	var rd io.Reader
	gz := false
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		if path == "/v1/sync/events" { // uploads are always gzip-compressed
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			if _, err := zw.Write(raw); err != nil {
				return err
			}
			if err := zw.Close(); err != nil {
				return err
			}
			raw, gz = buf.Bytes(), true
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if gz {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		se := &StatusError{Code: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil {
			se.Message = e.Error
		}
		return se
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("sync service: unreadable %s response: %w", path, err)
	}
	return nil
}

// retryAfter parses Retry-After as seconds or an HTTP date.
func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
