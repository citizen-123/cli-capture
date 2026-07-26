package protocol

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// TestFrameRoundTrip encodes then decodes frames across the three length
// encodings (7-bit, 16-bit, 64-bit) and both masking states, asserting the
// payload survives intact.
func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		masked  bool
		opcode  byte
		payload []byte
	}{
		{"short unmasked text", false, opText, []byte("hello")},
		{"short masked text", true, opText, []byte("hello")},
		{"16-bit length", true, opBinary, bytes.Repeat([]byte{0xAB}, 200)},
		{"64-bit length", false, opBinary, bytes.Repeat([]byte{0xCD}, 70000)},
		{"empty close", true, opClose, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &wsFrame{Fin: true, Opcode: tc.opcode, Masked: tc.masked, MaskKey: [4]byte{1, 2, 3, 4}, Payload: tc.payload}
			r := bufio.NewReader(bytes.NewReader(in.encode()))
			out, err := readWSFrame(r)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Opcode != tc.opcode || out.Fin != true || out.Masked != tc.masked {
				t.Errorf("header mismatch: got fin=%v op=%x masked=%v", out.Fin, out.Opcode, out.Masked)
			}
			if !bytes.Equal(out.Payload, tc.payload) {
				t.Errorf("payload mismatch: got %v want %v", out.Payload, tc.payload)
			}
		})
	}
}

// TestMaskIsInvolution: applying the mask twice yields the original bytes.
func TestMaskIsInvolution(t *testing.T) {
	key := [4]byte{0xDE, 0xAD, 0xBE, 0xEF}
	orig := []byte("the quick brown fox")
	buf := append([]byte(nil), orig...)
	wsMask(buf, key)
	if bytes.Equal(buf, orig) {
		t.Fatal("masking should change the bytes")
	}
	wsMask(buf, key)
	if !bytes.Equal(buf, orig) {
		t.Fatal("double mask should restore the bytes")
	}
}

func TestParseStatusCode(t *testing.T) {
	cases := map[string]int{
		"HTTP/1.1 101 Switching Protocols\r\n": 101,
		"HTTP/1.1 200 OK\r\n":                  200,
		"garbage":                              0,
	}
	for line, want := range cases {
		if got := parseStatusCode(line); got != want {
			t.Errorf("parseStatusCode(%q) = %d, want %d", strings.TrimSpace(line), got, want)
		}
	}
}
