package capture

import (
	"bytes"
	"compress/flate"
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

func rawDeflate(s string) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
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
		{"gzip, br", br(string(gz(payload)))},
		{"deflate", rawDeflate(payload)},
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

func TestEncodeContentEncodingRoundTrip(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip", "x-gzip", "deflate", "br", "zstd", "gzip, br", "gzip, gzip"} {
		t.Run(encoding, func(t *testing.T) {
			wire, err := EncodeContentEncoding([]byte(payload), encoding)
			if err != nil {
				t.Fatalf("EncodeContentEncoding: %v", err)
			}
			logical, err := DecodeContentEncodingStrict(wire, encoding)
			if err != nil {
				t.Fatalf("DecodeContentEncodingStrict: %v", err)
			}
			if string(logical) != payload {
				t.Errorf("round-trip = %q, want %q", logical, payload)
			}
		})
	}
}

func TestContentEncodingKeepsEmptyEntityEmpty(t *testing.T) {
	for _, encoding := range []string{"gzip", "gzip, br"} {
		if wire, err := EncodeContentEncoding(nil, encoding); err != nil || len(wire) != 0 {
			t.Errorf("EncodeContentEncoding(nil, %q) = %x, %v", encoding, wire, err)
		}
		if logical, err := DecodeContentEncodingStrict(nil, encoding); err != nil || len(logical) != 0 {
			t.Errorf("DecodeContentEncodingStrict(nil, %q) = %x, %v", encoding, logical, err)
		}
	}
}

func TestEncodeContentEncodingRejectsUnknown(t *testing.T) {
	if _, err := EncodeContentEncoding([]byte(payload), "compress"); err == nil {
		t.Fatal("expected unsupported Content-Encoding to fail")
	}
}
