package repeater

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// client goes straight to the origin (like replay), verifying against the system
// trust store.
var client = &http.Client{Timeout: 30 * time.Second}

// Send renders the template with vars, sends it, and captures the exchange as a
// new Flow — so a repeated request shows up in the traffic list and can be
// inspected, exported, or repeated again. This is the single-shot primitive that
// Repeater calls once and Sniper will call in a loop.
func Send(t *Template, vars map[string]string) (*capture.Flow, error) {
	req, err := t.Render(vars)
	if err != nil {
		return nil, err
	}

	nf := capture.NewFlow("repeater", authority(t.URL))
	nf.Secure = t.Secure
	nf.Protocol = capture.ProtoHTTP1
	nf.Request = &capture.Message{
		Direction: capture.ClientToServer,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s %s (repeat)", req.Method, req.URL.RequestURI()),
		Headers:   req.Header.Clone(),
		Body:      requestBody(req),
		Meta:      map[string]string{"method": req.Method, "path": req.URL.RequestURI()},
	}

	resp, err := client.Do(req)
	if err != nil {
		nf.Status = capture.StatusError
		nf.Err = err
		return nf, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	nf.Response = &capture.Message{
		Direction: capture.ServerToClient,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s (%d bytes, repeat)", resp.Status, len(body)),
		Headers:   resp.Header,
		Body:      capture.DecodeContentEncoding(body, resp.Header.Get("Content-Encoding")),
		Raw:       body,
		Meta:      map[string]string{"status": resp.Status},
	}
	nf.Status = capture.StatusComplete
	return nf, nil
}

// requestBody re-reads the rendered body via the request's GetBody (set by
// http.NewRequest for byte readers), so the captured flow records what was sent.
func requestBody(req *http.Request) []byte {
	if req.GetBody == nil {
		return nil
	}
	rc, err := req.GetBody()
	if err != nil {
		return nil
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return b
}

func authority(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}
