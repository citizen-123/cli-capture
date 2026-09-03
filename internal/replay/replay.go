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
	"strings"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
)

// client goes straight to the origin server (not back through the proxy), so it
// verifies against the system trust store like any normal client.
var client = &http.Client{Timeout: 30 * time.Second}

// Resend reconstructs f's request, sends it again, and returns the newly
// captured flow. The original flow is left untouched.
func Resend(f *capture.Flow) (*capture.Flow, error) {
	if f == nil || f.Request == nil {
		return nil, fmt.Errorf("replay: flow has no request to resend")
	}
	switch f.Protocol {
	case capture.ProtoHTTP1, capture.ProtoHTTP2, capture.ProtoGRPC:
	default:
		return nil, fmt.Errorf("replay: %s flows are not resendable", f.Protocol)
	}

	logicalBody, err := protocol.CapturedRequestBody(f)
	if err != nil {
		return nil, fmt.Errorf("replay: reconstruct request body: %w", err)
	}
	wireBody, err := protocol.EncodeRequestBody(f.Protocol, http.Header(f.Request.Headers), logicalBody)
	if err != nil {
		return nil, fmt.Errorf("replay: reconstruct request body: %w", err)
	}
	if len(wireBody) > capture.MaxRetainedWireBodyBytes {
		return nil, fmt.Errorf("replay: request body exceeds %d-byte limit", capture.MaxRetainedWireBodyBytes)
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

	req, err := http.NewRequest(method, url, bytes.NewReader(wireBody))
	if err != nil {
		return nil, err
	}
	// Copy headers, but let the client own Host/Content-Length so a stale value
	// from the capture can't desync the resend.
	for k, vv := range f.Request.Headers {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
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
	nf.Request = replayedRequest(req, logicalBody)
	nf.Messages = replayedGRPCMessages(logicalBody)

	resp, err := client.Do(req)
	if err != nil {
		nf.Status = capture.StatusError
		nf.Err = err
		return nf, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, capture.MaxRetainedWireBodyBytes+1))
	_ = resp.Body.Close()
	if readErr != nil {
		err = fmt.Errorf("replay: read response body: %w", readErr)
		nf.Status = capture.StatusError
		nf.Err = err
		return nf, err
	}
	truncated := len(body) > capture.MaxRetainedWireBodyBytes
	if truncated {
		body = body[:capture.MaxRetainedWireBodyBytes]
		nf.Truncated = true
	}
	nf.Response = &capture.Message{
		Direction: capture.ServerToClient,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s (%d bytes, replay)", resp.Status, len(body)),
		Headers:   resp.Header,
		Body:      body,
		Raw:       body,
		Truncated: truncated,
		Meta:      map[string]string{"status": resp.Status},
	}
	nf.Status = capture.StatusComplete
	return nf, nil
}

func replayedRequest(req *http.Request, body protocol.RequestBody) *capture.Message {
	message := &capture.Message{
		Direction: capture.ClientToServer,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s %s (replay)", req.Method, req.URL.RequestURI()),
		Headers:   req.Header.Clone(),
		Body:      append([]byte(nil), body.Body...),
		Meta: map[string]string{
			"method":                        req.Method,
			"path":                          req.URL.RequestURI(),
			protocol.BodyRepresentationMeta: protocol.BodyRepresentationDecoded,
		},
	}
	return message
}

func replayedGRPCMessages(body protocol.RequestBody) []*capture.Message {
	messages := make([]*capture.Message, 0, len(body.Messages))
	for _, grpcMessage := range body.Messages {
		meta := map[string]string{}
		if grpcMessage.Compressed {
			meta["compressed"] = "true"
		}
		payload := append([]byte(nil), grpcMessage.Data...)
		messages = append(messages, &capture.Message{
			Direction: capture.ClientToServer,
			Timestamp: time.Now(),
			Body:      payload,
			Raw:       append([]byte(nil), payload...),
			Meta:      meta,
		})
	}
	return messages
}
