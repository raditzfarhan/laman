// Package client talks to the running laman daemon's control API.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"laman/internal/config"
)

// Header must be set to "1" on every control API request; browsers cannot
// add it to a cross-site request without a CORS preflight, which the daemon
// never approves.
const Header = "X-Laman-Client"

var httpClient = &http.Client{Timeout: 10 * time.Second}

// BaseURL is the daemon's control address.
func BaseURL() string {
	cfg, err := config.Load()
	port := config.Default().ControlPort
	if err == nil {
		port = cfg.ControlPort
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// Do sends a request with an optional JSON body and returns the response body.
func Do(method, path string, body any) ([]byte, error) {
	return DoWithin(httpClient.Timeout, method, path, body)
}

// DoWithin is Do for requests that may take longer, such as opening a tunnel.
func DoWithin(timeout time.Duration, method, path string, body any) ([]byte, error) {
	httpClient := &http.Client{Timeout: timeout}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, BaseURL()+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set(Header, "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s", method, path, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Running reports whether the daemon answers.
func Running() bool {
	_, err := Do("GET", "/status", nil)
	return err == nil
}
