package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// frameGRPC builds a gRPC length-prefixed message.
func frameGRPC(payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = 0 // uncompressed
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

// TestGRPCFramerParsesAcrossReads feeds two messages through the framer using a
// deliberately tiny read buffer, so message boundaries fall mid-read — proving
// the framer reassembles from the accumulation buffer, not per-Read.
func TestGRPCFramerParsesAcrossReads(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(frameGRPC([]byte("first")))
	stream.Write(frameGRPC([]byte("second-and-longer")))

	var got [][]byte
	fr := NewGRPCFramer(&stream, func(m GRPCMessage) {
		got = append(got, append([]byte(nil), m.Data...))
	})

	// Drain with a 3-byte buffer to force fragmentation.
	buf := make([]byte, 3)
	for {
		_, err := fr.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}

	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got))
	}
	if string(got[0]) != "first" || string(got[1]) != "second-and-longer" {
		t.Errorf("messages mismatch: %q, %q", got[0], got[1])
	}
}

// TestGRPCFramerPassesBytesThrough: the framer must not alter the stream.
func TestGRPCFramerPassesBytesThrough(t *testing.T) {
	orig := append(frameGRPC([]byte("abc")), frameGRPC([]byte("de"))...)
	fr := NewGRPCFramer(bytes.NewReader(orig), func(GRPCMessage) {})
	out, err := io.ReadAll(fr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, orig) {
		t.Errorf("framer altered the stream")
	}
}

// TestGRPCTamperReaderEditsAndDrops streams three messages through the tamper
// reader: one kept, one dropped, one edited — and verifies the re-framed output.
func TestGRPCTamperReaderEditsAndDrops(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(frameGRPC([]byte("keep")))
	stream.Write(frameGRPC([]byte("drop")))
	stream.Write(frameGRPC([]byte("edit")))

	r := NewGRPCTamperReader(&stream, func(m GRPCMessage) ([]byte, bool) {
		switch string(m.Data) {
		case "drop":
			return nil, true
		case "edit":
			return []byte("EDITED"), false
		default:
			return nil, false
		}
	})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	fr := NewGRPCFramer(bytes.NewReader(out), func(m GRPCMessage) {
		got = append(got, string(m.Data))
	})
	io.Copy(io.Discard, fr)
	if len(got) != 2 || got[0] != "keep" || got[1] != "EDITED" {
		t.Errorf("re-framed messages = %v, want [keep EDITED]", got)
	}
}

func TestGRPCFrameCodecPreservesOrderAndCompression(t *testing.T) {
	want := []GRPCMessage{
		{Data: []byte("first")},
		{Compressed: true, Data: []byte("compressed-representation")},
		{Data: []byte{}},
	}
	wire, err := EncodeGRPCFrames(want)
	if err != nil {
		t.Fatalf("EncodeGRPCFrames: %v", err)
	}
	got, err := DecodeGRPCFrames(wire)
	if err != nil {
		t.Fatalf("DecodeGRPCFrames: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Compressed != want[i].Compressed || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDecodeGRPCFramesRejectsUnsupportedRepresentation(t *testing.T) {
	if _, err := DecodeGRPCFrames([]byte{2, 0, 0, 0, 0}); err == nil {
		t.Fatal("expected invalid compression flag to fail")
	}
	if _, err := DecodeGRPCFrames([]byte{0, 0, 0, 0, 2, 1}); err == nil {
		t.Fatal("expected partial payload to fail")
	}
}

func TestProtoFieldSummary(t *testing.T) {
	// field #1, wire 2 (len), value "hi"; field #2, wire 0 (varint), value 5.
	msg := []byte{
		0x0A, 0x02, 'h', 'i', // 1<<3|2 = 0x0A, len 2
		0x10, 0x05, // 2<<3|0 = 0x10, varint 5
	}
	got := ProtoFieldSummary(msg)
	want := "#1(len) #2(varint)"
	if got != want {
		t.Errorf("ProtoFieldSummary = %q, want %q", got, want)
	}
}
