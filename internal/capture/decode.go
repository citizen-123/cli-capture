package capture

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// maxDecoded aliases the shared logical-body budget so decompression cannot
// retain more data than the capture model accepts.
const maxDecoded = MaxRetainedLogicalBodyBytes

// DecodeContentEncoding decompresses a body per its Content-Encoding header
// (gzip, deflate, br, zstd). Empty/identity/unknown encodings and any decode
// error return the input unchanged, so display is always best-effort and never
// fails.
func DecodeContentEncoding(body []byte, encoding string) []byte {
	out, err := DecodeContentEncodingStrict(body, encoding)
	if err != nil || len(out) == 0 {
		return body
	}
	return out
}

// DecodeContentEncodingStrict decompresses a body per Content-Encoding and
// reports unsupported encodings or invalid compressed data. It is intended for
// operations, such as replay, that must not mistake wire bytes for logical
// display bytes.
func DecodeContentEncodingStrict(body []byte, encoding string) ([]byte, error) {
	if len(body) > MaxRetainedWireBodyBytes {
		return nil, fmt.Errorf("wire body exceeds %d bytes", MaxRetainedWireBodyBytes)
	}
	encodings, err := contentEncodings(encoding)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return body, nil
	}
	out := body
	for i := len(encodings) - 1; i >= 0; i-- {
		out, err = decodeOneContentEncoding(out, encodings[i])
		if err != nil {
			return nil, fmt.Errorf("decode Content-Encoding %q: %w", encodings[i], err)
		}
	}
	return out, nil
}

func decodeOneContentEncoding(body []byte, encoding string) ([]byte, error) {
	switch encoding {
	case "identity":
		return body, nil
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readDecoded(r)
	case "deflate":
		// "deflate" is ambiguous: accept both zlib-wrapped and raw streams.
		if r, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			defer r.Close()
			return readDecoded(r)
		}
		r := flate.NewReader(bytes.NewReader(body))
		defer r.Close()
		return readDecoded(r)
	case "br":
		return readDecoded(brotli.NewReader(bytes.NewReader(body)))
	case "zstd":
		d, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer d.Close()
		return readDecoded(d)
	}
	panic("validated Content-Encoding reached decoder")
}

// EncodeContentEncoding converts logical body bytes back to the wire
// representation named by Content-Encoding.
func EncodeContentEncoding(body []byte, encoding string) ([]byte, error) {
	if len(body) > MaxRetainedLogicalBodyBytes {
		return nil, fmt.Errorf("logical body exceeds %d bytes", MaxRetainedLogicalBodyBytes)
	}
	encodings, err := contentEncodings(encoding)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	out := body
	for _, current := range encodings {
		out, err = encodeOneContentEncoding(out, current)
		if err != nil {
			return nil, fmt.Errorf("encode Content-Encoding %q: %w", current, err)
		}
		if len(out) > MaxRetainedWireBodyBytes {
			return nil, fmt.Errorf("encoded body exceeds %d bytes", MaxRetainedWireBodyBytes)
		}
	}
	return out, nil
}

func encodeOneContentEncoding(body []byte, encoding string) ([]byte, error) {
	if encoding == "identity" {
		return body, nil
	}
	var buf bytes.Buffer
	var w io.WriteCloser
	var err error
	switch encoding {
	case "gzip", "x-gzip":
		w = gzip.NewWriter(&buf)
	case "deflate":
		w = zlib.NewWriter(&buf)
	case "br":
		w = brotli.NewWriter(&buf)
	case "zstd":
		w, err = zstd.NewWriter(&buf)
	default:
		panic("validated Content-Encoding reached encoder")
	}
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func contentEncodings(header string) ([]string, error) {
	if strings.TrimSpace(header) == "" {
		return nil, nil
	}
	parts := strings.Split(header, ",")
	encodings := make([]string, 0, len(parts))
	for _, part := range parts {
		encoding := strings.ToLower(strings.TrimSpace(part))
		switch encoding {
		case "identity", "gzip", "x-gzip", "deflate", "br", "zstd":
			encodings = append(encodings, encoding)
		case "":
			return nil, fmt.Errorf("invalid empty Content-Encoding")
		default:
			return nil, fmt.Errorf("unsupported Content-Encoding %q", strings.TrimSpace(part))
		}
	}
	return encodings, nil
}

func readDecoded(r io.Reader) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, maxDecoded+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxDecoded {
		return nil, fmt.Errorf("decoded body exceeds %d bytes", maxDecoded)
	}
	return out, nil
}
