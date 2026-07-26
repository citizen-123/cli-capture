package capture

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

const payload = `{"servers":[{"name":"a"},{"name":"b"}]}`

func gz(s string) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.Bytes()
}

func br(s string) []byte {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.Bytes()
}

func zs(s string) []byte {
	var buf bytes.Buffer
	w, _ := zstd.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.Bytes()
}

func TestDecodeContentEncoding(t *testing.T) {
	cases := []struct {
		enc  string
		body []byte
	}{
		{"gzip", gz(payload)},
		{"br", br(payload)},
		{"zstd", zs(payload)},
	}
	for _, tc := range cases {
		t.Run(tc.enc, func(t *testing.T) {
			got := DecodeContentEncoding(tc.body, tc.enc)
			if string(got) != payload {
				t.Errorf("%s decode = %q, want %q", tc.enc, got, payload)
			}
			// The compressed bytes should differ from the decoded output.
			if bytes.Equal(got, tc.body) {
				t.Errorf("%s: body was not actually decompressed", tc.enc)
			}
		})
	}
}

func TestDecodeContentEncodingPassthrough(t *testing.T) {
	plain := []byte("not compressed")
	if got := DecodeContentEncoding(plain, ""); !bytes.Equal(got, plain) {
		t.Error("empty encoding should pass through")
	}
	if got := DecodeContentEncoding(plain, "identity"); !bytes.Equal(got, plain) {
		t.Error("identity encoding should pass through")
	}
	// Garbage claiming to be gzip must fall back to the original, not error.
	if got := DecodeContentEncoding(plain, "gzip"); !bytes.Equal(got, plain) {
		t.Errorf("invalid gzip should fall back to original, got %q", got)
	}
}
