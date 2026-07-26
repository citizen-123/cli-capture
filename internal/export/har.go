package export

import (
	"encoding/json"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// HAR 1.2 subset — enough for the common consumers (browsers, Postman, etc.).
type harArchive struct {
	Log harLog `json:"log"`
}

type harLog struct {
	Version string     `json:"version"`
	Creator harCreator `json:"creator"`
	Entries []harEntry `json:"entries"`
}

type harCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type harEntry struct {
	StartedDateTime string      `json:"startedDateTime"`
	Time            int         `json:"time"`
	Request         harRequest  `json:"request"`
	Response        harResponse `json:"response"`
	Cache           struct{}    `json:"cache"`
	Timings         harTimings  `json:"timings"`
}

type harTimings struct {
	Send    int `json:"send"`
	Wait    int `json:"wait"`
	Receive int `json:"receive"`
}

type harNVP struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harRequest struct {
	Method      string   `json:"method"`
	URL         string   `json:"url"`
	HTTPVersion string   `json:"httpVersion"`
	Headers     []harNVP `json:"headers"`
	QueryString []harNVP `json:"queryString"`
	PostData    *harPost `json:"postData,omitempty"`
	HeadersSize int      `json:"headersSize"`
	BodySize    int      `json:"bodySize"`
}

type harPost struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type harContent struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
}

type harResponse struct {
	Status      int        `json:"status"`
	StatusText  string     `json:"statusText"`
	HTTPVersion string     `json:"httpVersion"`
	Headers     []harNVP   `json:"headers"`
	Content     harContent `json:"content"`
	HeadersSize int        `json:"headersSize"`
	BodySize    int        `json:"bodySize"`
}

// HAR renders the exportable flows as a HAR 1.2 archive. Non-HTTP flows (raw
// TCP, WebSocket) are skipped.
func HAR(flows []*capture.Flow) ([]byte, error) {
	entries := make([]harEntry, 0, len(flows))
	for _, f := range flows {
		if !exportable(f) {
			continue
		}
		entries = append(entries, entryFor(f))
	}
	arc := harArchive{Log: harLog{
		Version: "1.2",
		Creator: harCreator{Name: "cli-capture", Version: "0.1"},
		Entries: entries,
	}}
	return json.MarshalIndent(arc, "", "  ")
}

func entryFor(f *capture.Flow) harEntry {
	e := harEntry{
		StartedDateTime: f.StartedAt.UTC().Format(time.RFC3339),
		Request: harRequest{
			Method:      method(f),
			URL:         flowURL(f),
			HTTPVersion: string(f.Protocol),
			Headers:     harHeaders(f.Request),
			QueryString: []harNVP{},
			HeadersSize: -1,
			BodySize:    len(f.Request.Body),
		},
		Response: harResponse{HTTPVersion: string(f.Protocol), Headers: []harNVP{}, HeadersSize: -1, BodySize: -1},
	}
	if len(f.Request.Body) > 0 {
		e.Request.PostData = &harPost{
			MimeType: headerValue(f.Request, "Content-Type"),
			Text:     string(f.Request.Body),
		}
	}
	if f.Response != nil {
		e.Response.Status = statusCode(f.Response)
		e.Response.StatusText = f.Response.Meta["status"]
		e.Response.Headers = harHeaders(f.Response)
		e.Response.Content = harContent{
			Size:     len(f.Response.Body),
			MimeType: headerValue(f.Response, "Content-Type"),
			Text:     string(f.Response.Body),
		}
		e.Response.BodySize = len(f.Response.Body)
	}
	return e
}

func harHeaders(m *capture.Message) []harNVP {
	out := []harNVP{}
	for _, k := range sortedKeys(m.Headers) {
		for _, v := range m.Headers[k] {
			out = append(out, harNVP{Name: k, Value: v})
		}
	}
	return out
}

func headerValue(m *capture.Message, key string) string {
	if vv := m.Headers[key]; len(vv) > 0 {
		return vv[0]
	}
	return ""
}

// statusCode extracts the leading integer from a "200 OK"-style status string.
func statusCode(m *capture.Message) int {
	s := m.Meta["status"]
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
