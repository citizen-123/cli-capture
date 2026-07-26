package proxy_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/proxy"
	"github.com/citizen-123/cli-capture/internal/proxy/ca"
)

// TestGzipResponseDecoded reproduces the screenshot case: an upstream returns
// gzipped JSON, and the captured flow's response body must be the *decoded* JSON
// (for display), while the client still receives the compressed bytes.
func TestGzipResponseDecoded(t *testing.T) {
	const body = `{"servers":[{"name":"a"},{"name":"b"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		gz.Write([]byte(body))
		gz.Close()
	}))
	defer upstream.Close()

	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := capture.NewStore()
	px := proxy.New(store, authority, intercept.NewEngine())
	if err := px.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	go px.Serve()
	defer px.Close()

	proxyURL, _ := url.Parse("http://" + px.Addr())
	// DisableCompression so the client doesn't strip Content-Encoding before we
	// see it; we want to prove the *proxy* decodes for capture.
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableCompression: true}, Timeout: 5 * time.Second}
	resp, err := client.Get(upstream.URL + "/v1/mcp_servers")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	flows := waitForFlows(store, 1, time.Second)
	if len(flows) != 1 || flows[0].Response == nil {
		t.Fatal("no captured response")
	}
	if got := string(flows[0].Response.Body); got != body {
		t.Errorf("captured response body = %q, want decoded JSON %q", got, body)
	}
}

// TestPlainHTTPCapture drives a real HTTP request through the proxy and asserts
// the exchange is captured and forwarded intact. This exercises the whole
// transport→detect→HTTP1→store path end to end (plaintext, so no CA trust setup
// is needed on the upstream side).
func TestPlainHTTPCapture(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "ok")
		io.WriteString(w, "hello from upstream")
	}))
	defer upstream.Close()

	authority, err := ca.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	store := capture.NewStore()
	engine := intercept.NewEngine() // interception off by default

	px := proxy.New(store, authority, engine)
	if err := px.Listen("127.0.0.1:0"); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go px.Serve()
	defer px.Close()

	proxyURL, _ := url.Parse("http://" + px.Addr())
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}

	resp, err := client.Get(upstream.URL + "/hello")
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if got := string(body); got != "hello from upstream" {
		t.Fatalf("body not forwarded intact: %q", got)
	}
	if resp.Header.Get("X-Test") != "ok" {
		t.Fatalf("response header lost")
	}

	flows := waitForFlows(store, 1, time.Second)
	if len(flows) != 1 {
		t.Fatalf("want 1 captured flow, got %d", len(flows))
	}
	f := flows[0]
	if f.Protocol != capture.ProtoHTTP1 {
		t.Errorf("protocol = %s, want http/1.1", f.Protocol)
	}
	if f.Request == nil || f.Request.Meta["method"] != "GET" {
		t.Errorf("request not captured: %+v", f.Request)
	}
	if f.Response == nil {
		t.Errorf("response not captured")
	}
}

func waitForFlows(s *capture.Store, n int, d time.Duration) []*capture.Flow {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fs := s.List(); len(fs) >= n {
			return fs
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.List()
}
