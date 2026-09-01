package repeater

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/protocol"
)

// client goes straight to the origin (like replay), verifying against the system
// trust store.
var client = &http.Client{Timeout: 30 * time.Second}

// Send renders the template with vars, sends it, and captures the exchange as a
// new Flow — so a repeated request shows up in the traffic list and can be
// inspected, exported, or repeated again. This is the single-shot primitive that
// Repeater calls once and Sniper will call in a loop.
func Send(t *Template, vars map[string]string) (*capture.Flow, error) {
	req, logicalBody, proto, err := t.render(vars)
	if err != nil {
		return nil, err
	}

	nf := capture.NewFlow("repeater", req.URL.Host)
	nf.Secure = t.Secure
	nf.Protocol = proto
	nf.Request = &capture.Message{
		Direction: capture.ClientToServer,
		Timestamp: time.Now(),
		Summary:   fmt.Sprintf("%s %s (repeat)", req.Method, req.URL.RequestURI()),
		Headers:   req.Header.Clone(),
		Body:      append([]byte(nil), logicalBody.Body...),
		Meta: map[string]string{
			"method":                        req.Method,
			"path":                          req.URL.RequestURI(),
			protocol.BodyRepresentationMeta: protocol.BodyRepresentationDecoded,
		},
	}
	nf.Messages = repeatedGRPCMessages(logicalBody)

	resp, err := client.Do(req)
	if err != nil {
		nf.Status = capture.StatusError
		nf.Err = err
		return nf, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		err = fmt.Errorf("repeater: read response body: %w", readErr)
		nf.Status = capture.StatusError
		nf.Err = err
		return nf, err
	}

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

func repeatedGRPCMessages(body protocol.RequestBody) []*capture.Message {
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
