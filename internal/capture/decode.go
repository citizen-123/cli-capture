package capture

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// maxDecoded caps a decompressed body so a compression bomb can't exhaust memory.
const maxDecoded = 64 << 20 // 64 MiB

// DecodeContentEncoding decompresses a body per its Content-Encoding header
// (gzip, deflate, br, zstd). Empty/identity/unknown encodings and any decode
// error return the input unchanged, so display is always best-effort and never
// fails.
func DecodeContentEncoding(body []byte, encoding string) []byte {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return body
		}
		defer r.Close()
		return readAllOr(r, body)
	case "deflate":
		// "deflate" is ambiguous: some servers send zlib-wrapped, some raw.
		if r, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			defer r.Close()
			return readAllOr(r, body)
		}
		fr := flate.NewReader(bytes.NewReader(body))
		defer fr.Close()
		return readAllOr(fr, body)
	case "br":
		return readAllOr(brotli.NewReader(bytes.NewReader(body)), body)
	case "zstd":
		d, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			return body
		}
		defer d.Close()
		return readAllOr(d, body)
	default:
		return body
	}
}

func readAllOr(r io.Reader, orig []byte) []byte {
	out, err := io.ReadAll(io.LimitReader(r, maxDecoded))
	if err != nil || len(out) == 0 {
		return orig // decode failed or produced nothing — keep the raw bytes
	}
	return out
}
