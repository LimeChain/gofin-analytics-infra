// Package function implements a provider-specific, allowlisted HTTP proxy.
package function

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
)

const (
	maxRequest            = 1 << 20
	maxResponse           = 4 << 20
	defaultCredentialFile = "/var/secrets/provider/api-key"
)

type route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}
type config struct {
	Upstream         string            `json:"upstream"`
	CredentialHeader string            `json:"credential_header"`
	CredentialPrefix string            `json:"credential_prefix"`
	CredentialFile   string            `json:"credential_file"`
	Routes           []route           `json:"routes"`
	QueryInjections  map[string]string `json:"query_injections"`
	ForwardHeaders   []string          `json:"forward_headers"`
}

func init() { functions.HTTP("Proxy", Proxy) }

// Disable connection reuse and HTTP/2 to avoid net/http's implicit retries of
// idempotent requests on reused connections. Redirects are never followed.
var client = &http.Client{
	Timeout:       30 * time.Second,
	Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func validPath(p string) bool {
	return strings.HasPrefix(p, "/") && p != "/" && path.Clean(p) == p && !strings.ContainsAny(p, "%\\?#")
}

func readConfig() (config, *url.URL, bool) {
	var c config
	dec := json.NewDecoder(strings.NewReader(os.Getenv("PROXY_CONFIG")))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil {
		return c, nil, false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return c, nil, false
	}
	u, err := url.Parse(c.Upstream)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.EscapedPath() != u.Path || (u.Path != "" && !validPath(u.Path)) {
		return c, nil, false
	}
	if c.CredentialHeader == "" || !validHeaderName(c.CredentialHeader) || strings.ContainsAny(c.CredentialPrefix, "\r\n") || len(c.Routes) == 0 {
		return c, nil, false
	}
	if c.CredentialFile == "" {
		c.CredentialFile = defaultCredentialFile
	}
	for _, r := range c.Routes {
		if (r.Method != "GET" && r.Method != "POST") || !validPath(r.Path) {
			return c, nil, false
		}
	}
	for _, h := range c.ForwardHeaders {
		if !safeForwardHeader(h, c.CredentialHeader) {
			return c, nil, false
		}
	}
	for k, v := range c.QueryInjections {
		if k == "" || v == "" {
			return c, nil, false
		}
	}
	return c, u, true
}

func validHeaderName(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return s != ""
}

func safeForwardHeader(name, credentialHeader string) bool {
	if !validHeaderName(name) || strings.EqualFold(name, credentialHeader) {
		return false
	}
	lower := strings.ToLower(name)
	for _, fragment := range []string{"authorization", "cookie", "token", "api-key", "api_key", "apikey", "secret", "trace"} {
		if strings.Contains(lower, fragment) {
			return false
		}
	}
	if strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "proxy-") {
		return false
	}
	switch lower {
	case "connection", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade", "host", "content-length", "content-encoding", "accept-encoding", "forwarded", "x-real-ip", "baggage":
		return false
	}
	return true
}

func secret(file string) (string, bool) {
	b, err := os.ReadFile(file)
	s := strings.TrimSpace(string(b))
	return s, err == nil && s != "" && s != "UNCONFIGURED" && !strings.ContainsAny(s, "\r\n")
}

// hopHeader reports fields named by a Connection header, which are scoped to
// that hop even if their names are normally on an allowlist.
func hopHeader(headers http.Header, name string) bool {
	for _, value := range headers.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), name) {
				return true
			}
		}
	}
	return false
}

var requestHeaders = []string{"Accept", "Content-Type"}
var responseHeaders = []string{"Content-Type", "Retry-After", "Cache-Control"}

// Proxy forwards only configured provider routes and injects mounted credentials.
func Proxy(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" && r.URL.RawPath == "" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	c, u, ok := readConfig()
	if !ok {
		http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.URL.RawPath != "" || !validPath(r.URL.Path) || r.URL.EscapedPath() != r.URL.Path {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	allowed := false
	for _, route := range c.Routes {
		if route.Method == r.Method && route.Path == r.URL.Path {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	for _, encoding := range r.Header.Values("Content-Encoding") {
		if !strings.EqualFold(strings.TrimSpace(encoding), "identity") {
			http.Error(w, "unsupported content encoding", http.StatusUnsupportedMediaType)
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequest+1))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if len(body) > maxRequest {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	key, ok := secret(c.CredentialFile)
	if !ok {
		http.Error(w, "provider not configured", http.StatusServiceUnavailable)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "invalid query", http.StatusBadRequest)
		return
	}
	for k := range q {
		switch strings.ToLower(k) {
		case "api_key", "apikey", "api-key", "x-api-key", "access_token", "authorization":
			delete(q, k)
		}
	}
	for k, file := range c.QueryInjections {
		value, ok := secret(file)
		if !ok {
			http.Error(w, "provider not configured", http.StatusServiceUnavailable)
			return
		}
		// Replace all case variants, including duplicate values, of attribution keys.
		for existing := range q {
			if strings.EqualFold(existing, k) {
				delete(q, existing)
			}
		}
		q.Set(k, value)
	}
	u.Path += r.URL.Path
	u.RawQuery = q.Encode()
	// An opaque reader avoids populating GetBody, preventing request replay.
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	req.ContentLength = int64(len(body))
	seenHeaders := make(map[string]bool)
	for _, h := range append(append([]string{}, requestHeaders...), c.ForwardHeaders...) {
		canonical := strings.ToLower(h)
		if seenHeaders[canonical] {
			continue
		}
		seenHeaders[canonical] = true
		if hopHeader(r.Header, h) {
			continue
		}
		for _, v := range r.Header.Values(h) {
			req.Header.Add(h, v)
		}
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set(c.CredentialHeader, c.CredentialPrefix+key)
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		http.Error(w, "invalid upstream response", http.StatusBadGateway)
		return
	}
	result, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(result) > maxResponse || bytes.Contains(result, []byte(key)) {
		http.Error(w, "invalid upstream response", http.StatusBadGateway)
		return
	}
	for _, h := range responseHeaders {
		if hopHeader(resp.Header, h) {
			continue
		}
		for _, v := range resp.Header.Values(h) {
			if !strings.Contains(v, key) {
				w.Header().Add(h, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(result)
}
