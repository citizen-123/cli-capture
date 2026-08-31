// Package export renders captured flows into portable formats: a curl command
// for a single request, and a HAR 1.2 archive for a whole session. Only
// request/response protocols (HTTP/1, HTTP/2, gRPC) are exportable.
package export

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/shellquote"
)

func exportable(f *capture.Flow) bool {
	switch f.Protocol {
	case capture.ProtoHTTP1, capture.ProtoHTTP2, capture.ProtoGRPC:
		return f.Request != nil
	default:
		return false
	}
}

func flowURL(f *capture.Flow) string {
	scheme := "http"
	if f.Secure {
		scheme = "https"
	}
	host := f.ServerAddr
	if h, p, err := net.SplitHostPort(host); err == nil {
		if (scheme == "https" && p == "443") || (scheme == "http" && p == "80") {
			host = h // drop the default port for a clean URL
		}
	}
	return scheme + "://" + host + f.Request.Meta["path"]
}

func method(f *capture.Flow) string {
	if m := f.Request.Meta["method"]; m != "" {
		return m
	}
	return "GET"
}

// Curl renders a flow's request as a copy-pasteable curl command.
func Curl(f *capture.Flow) (string, error) {
	if !exportable(f) {
		return "", fmt.Errorf("export: %s flows can't be exported as curl", f.Protocol)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "curl -X %s %s", method(f), shellquote.Single(flowURL(f)))
	for _, k := range sortedKeys(f.Request.Headers) {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range f.Request.Headers[k] {
			fmt.Fprintf(&b, " \\\n  -H %s", shellquote.Single(k+": "+v))
		}
	}
	if len(f.Request.Body) > 0 {
		fmt.Fprintf(&b, " \\\n  --data-binary %s", shellquote.Single(string(f.Request.Body)))
	}
	return b.String(), nil
}

func sortedKeys(h map[string][]string) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
