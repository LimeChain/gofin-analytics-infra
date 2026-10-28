package function

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, upstream string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	key, id := filepath.Join(dir, "key"), filepath.Join(dir, "id")
	if err := os.WriteFile(key, []byte("secret-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(id, []byte("attribution"), 0600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(config{Upstream: upstream + "/v1", CredentialHeader: "X-Api-Key", CredentialFile: key, Routes: []route{{"POST", "/quote"}, {"GET", "/status"}}, QueryInjections: map[string]string{"integratorId": id}})
	t.Setenv("PROXY_CONFIG", string(b))
	return key, id
}
func invoke(method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer caller")
	req.Header.Set("Cookie", "caller=private")
	req.Header.Set("X-Api-Key", "caller-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Proxy(w, req)
	return w
}
func TestForwardRotationAndBoundaries(t *testing.T) {
	var requests int
	expectedKey := "secret-key"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/quote" || r.URL.Query().Get("integratorId") != "attribution" || len(r.URL.Query()["integratorId"]) != 1 || r.URL.Query().Get("other") != "yes" || r.URL.Query().Has("api_key") {
			t.Errorf("bad URL: %v", r.URL)
		}
		if r.Header.Get("X-Api-Key") != expectedKey || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("bad request headers")
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"amount":"1"}` {
			t.Errorf("bad body %q", b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Set-Cookie", "private")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"limited"}`))
	}))
	defer server.Close()
	old := client
	client = server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.Cleanup(func() { client = old })
	key, _ := setup(t, server.URL)
	for _, newKey := range []string{"secret-key", "rotated-key"} {
		expectedKey = newKey
		if err := os.WriteFile(key, []byte(newKey), 0600); err != nil {
			t.Fatal(err)
		}
		w := invoke("POST", "/quote?integratorId=caller&integratorId=other&INTEGRATORID=caller&other=yes&api_key=caller", `{"amount":"1"}`)
		if w.Code != 429 || w.Body.String() != `{"error":"limited"}` || w.Header().Get("Retry-After") != "7" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("response: %d %v %s", w.Code, w.Header(), w.Body)
		}
	}
	if requests != 2 {
		t.Fatalf("upstream requests = %d", requests)
	}
	if err := os.WriteFile(key, []byte("UNCONFIGURED"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := invoke("POST", "/quote", ""); w.Code != 503 || !strings.Contains(w.Body.String(), "provider not configured") {
		t.Fatalf("placeholder: %d", w.Code)
	}
	if requests != 2 {
		t.Fatal("forwarded placeholder")
	}
}
func TestValidationAndHealth(t *testing.T) {
	t.Setenv("PROXY_CONFIG", "bad")
	if w := invoke("GET", "/health", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := invoke("POST", "/quote", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	setup(t, "https://provider.example")
	for _, tc := range []struct{ method, path string }{{"GET", "/quote"}, {"POST", "/quote/../status"}, {"POST", "/%71uote"}, {"POST", "//quote"}, {"DELETE", "/quote"}} {
		if w := invoke(tc.method, tc.path, ""); w.Code != 404 {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	if w := invoke("POST", "/quote", strings.Repeat("x", maxRequest+1)); w.Code != 413 {
		t.Fatal(w.Code)
	}
}
func TestRedirectAndSecretResponse(t *testing.T) {
	count := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.URL.Path == "/v1/status" {
			w.Header().Set("Location", "https://attacker.invalid/")
			w.WriteHeader(302)
			return
		}
		_, _ = w.Write([]byte("secret-key"))
	}))
	defer server.Close()
	old := client
	client = server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.Cleanup(func() { client = old })
	setup(t, server.URL)
	if w := invoke("GET", "/status", ""); w.Code != 302 || w.Header().Get("Location") != "" {
		t.Fatalf("redirect %d %v", w.Code, w.Header())
	}
	if w := invoke("POST", "/quote", ""); w.Code != 502 || strings.Contains(w.Body.String(), "secret-key") {
		t.Fatal("secret leaked")
	}
	if count != 2 {
		t.Fatalf("requests: %d", count)
	}
}

func TestResponseLimitAndUnavailableAttribution(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponse+1)))
	}))
	defer server.Close()
	old := client
	client = server.Client()
	t.Cleanup(func() { client = old })
	_, id := setup(t, server.URL)
	if w := invoke("POST", "/quote", ""); w.Code != 502 {
		t.Fatalf("oversized response: %d", w.Code)
	}
	if err := os.WriteFile(id, []byte("UNCONFIGURED"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := invoke("POST", "/quote", ""); w.Code != 503 {
		t.Fatalf("missing attribution: %d", w.Code)
	}
	if calls != 1 {
		t.Fatalf("upstream calls: %d", calls)
	}
}

func TestRejectEncodedRequestAndStripConnectionHeaders(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Content-Type") != "" || r.Header.Get("Accept") != "" {
			t.Errorf("connection-scoped request headers forwarded: %v", r.Header)
		}
		w.Header().Set("Connection", "Retry-After")
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	old := client
	client = server.Client()
	t.Cleanup(func() { client = old })
	setup(t, server.URL)
	req := httptest.NewRequest("POST", "/quote", strings.NewReader("compressed bytes"))
	req.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	Proxy(w, req)
	if w.Code != 415 || calls != 0 {
		t.Fatalf("encoded request: status %d, calls %d", w.Code, calls)
	}
	req = httptest.NewRequest("POST", "/quote", strings.NewReader("{}"))
	req.Header.Set("Connection", "Content-Type, accept")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w = httptest.NewRecorder()
	Proxy(w, req)
	if w.Code != 429 || calls != 1 || w.Header().Get("Retry-After") != "" {
		t.Fatalf("response %d, calls %d, headers %v", w.Code, calls, w.Header())
	}
}

func TestConfiguredForwardHeadersAndUnsafeNames(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Permit2-Disabled") != "true" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "secret-key" {
			t.Errorf("incorrect forwarded headers: %v", r.Header)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != "{\"arbitrary\":  42}" {
			t.Errorf("body changed: %q", b)
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	old := client
	client = server.Client()
	t.Cleanup(func() { client = old })
	setup(t, server.URL)
	var c config
	if err := json.Unmarshal([]byte(os.Getenv("PROXY_CONFIG")), &c); err != nil {
		t.Fatal(err)
	}
	setConfig := func() {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("PROXY_CONFIG", string(b))
	}
	c.ForwardHeaders = []string{"X-Permit2-Disabled"}
	setConfig()
	req := httptest.NewRequest("POST", "/quote", strings.NewReader("{\"arbitrary\":  42}"))
	req.Header.Set("X-Permit2-Disabled", "true")
	req.Header.Set("Authorization", "caller-token")
	w := httptest.NewRecorder()
	Proxy(w, req)
	if w.Code != 204 || calls != 1 {
		t.Fatalf("status %d, calls %d", w.Code, calls)
	}
	for _, name := range []string{"authorization", "X-Api-Key", "Cookie", "X-Forwarded-For", "Traceparent", "Connection", "Content-Length", "X-Access-Token", "Proxy-Authorization"} {
		c.ForwardHeaders = []string{name}
		setConfig()
		w := invoke("POST", "/quote", "{}")
		if w.Code != 503 || calls != 1 {
			t.Errorf("unsafe header %s accepted: status %d calls %d", name, w.Code, calls)
		}
	}
}

// Signed orders must be forwarded exactly once; status reads must retain the
// precise order identity so a caller can reconcile an uncertain submission.
func TestUniswapXSubmissionAndRecovery(t *testing.T) {
	const orderID = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const body = `{"routing":"DUTCH_V3","quote":{"orderId":"` + orderID + `"},"signature":"synthetic-signature"}`
	var posts, reads int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret-key" || r.Header.Get("Authorization") != "" {
			t.Error("credential isolation failed")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/order":
			posts++
			b, _ := io.ReadAll(r.Body)
			if string(b) != body {
				t.Errorf("signed body changed: %q", b)
			}
			// The upstream may accept an order before its response fails.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
		case "GET /v1/orders":
			reads++
			if r.URL.Query().Get("orderId") != orderID {
				t.Error("order identity changed")
			}
			_, _ = w.Write([]byte(`{"orders":[{"orderId":"` + orderID + `","orderStatus":"open"}]}`))
		default:
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL)
		}
	}))
	defer server.Close()
	old := client
	client = server.Client()
	t.Cleanup(func() { client = old })
	key, _ := setup(t, server.URL)
	b, err := json.Marshal(config{Upstream: server.URL + "/v1", CredentialHeader: "X-Api-Key", CredentialFile: key, Routes: []route{{"POST", "/order"}, {"GET", "/orders"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROXY_CONFIG", string(b))
	if w := invoke("POST", "/order", body); w.Code != 503 || w.Body.String() != `{"error":"temporary"}` || posts != 1 {
		t.Fatalf("submission: status %d, posts %d, body %s", w.Code, posts, w.Body)
	}
	if w := invoke("GET", "/orders?orderId="+orderID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"orderStatus":"open"`) || reads != 1 {
		t.Fatalf("recovery: status %d, reads %d", w.Code, reads)
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/order"}, {"POST", "/orders"}, {"DELETE", "/order"}, {"POST", "/cancel"}} {
		if w := invoke(tc.method, tc.path, body); w.Code != 404 {
			t.Errorf("unexpected allowed route %s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	if posts != 1 || reads != 1 {
		t.Fatalf("unexpected forwarding or retry: posts %d, reads %d", posts, reads)
	}
}
