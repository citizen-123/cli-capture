// Package replay resends a previously captured HTTP request to its original
// server and records the new exchange as a fresh flow. Only request/response
// protocols (HTTP/1, HTTP/2, gRPC) are resendable — raw TCP and WebSocket are
// not single request/response exchanges.
package replay

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// client goes straight to the origin server (not back through the proxy), so it
// verifies against the system trust store like any normal client.
var client = &http.Client{Timeout: 30 * time.Second}

// Resend reconstructs f's request, sends it again, and returns the newly
// captured flow. The original flow is left untouched.
func Resend(f *capture.Flow) (*capture.Flow, error) {
	if f.Request == nil {
		return nil, fmt.Errorf("replay: flow has no request to resend")
	}
	switch f.Protocol {
	case capture.ProtoHTTP1, capture.ProtoHTTP2, capture.ProtoGRPC:
	default:
		return nil, fmt.Errorf("replay: %s flows are not resendable", f.Protocol)
	}

	scheme := "http"
	if f.Secure {
		scheme = "https"
	}
	method := f.Request.Meta["method"]
	if method == "" {
		method = http.MethodGet
	}
	url := scheme + "://" + f.ServerAddr + f.Request.Meta["path"]

	req, err := http.NewRequest(method, url, bytes.NewReader(f.Request.Body))
	if err != nil {
		return nil, err
	}
	// Copy headers, but let the client own Host/Content-Length so a stale value
	// from the capture can't desync the resend.
	for k, vv := range f.Request.Headers {
		if k == "Host" || k == "Content-Length" {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	nf := capture.NewFlow("replay", f.ServerAddr)
	nf.Protocol = f.Protocol
	nf.SNI = f.SNI
	nf.Secure = f.Secure
	nf.Request = f.Request

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
		Summary:   fmt.Sprintf("%s (%d bytes, replay)", resp.Status, len(body)),
		Headers:   resp.Header,
		Body:      body,
		Raw:       body,
		Meta:      map[string]string{"status": resp.Status},
	}
	nf.Status = capture.StatusComplete
	return nf, nil
}
