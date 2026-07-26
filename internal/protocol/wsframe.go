package protocol

import (
	"bufio"
	"encoding/binary"
	"io"
)

// RFC 6455 opcodes.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// Exported opcodes for external injectors (see capture.Injector).
const (
	WSText   byte = opText
	WSBinary byte = opBinary
)

// wsFrame is a decoded WebSocket frame with its payload already unmasked.
type wsFrame struct {
	Fin     bool
	Opcode  byte
	Masked  bool
	MaskKey [4]byte
	Payload []byte
}

// readWSFrame decodes one frame, unmasking the payload if it was masked
// (client→server frames always are).
func readWSFrame(r *bufio.Reader) (*wsFrame, error) {
	b0, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	b1, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	fr := &wsFrame{
		Fin:    b0&0x80 != 0,
		Opcode: b0 & 0x0f,
		Masked: b1&0x80 != 0,
	}
	length := int(b1 & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}
	if fr.Masked {
		if _, err := io.ReadFull(r, fr.MaskKey[:]); err != nil {
			return nil, err
		}
	}
	fr.Payload = make([]byte, length)
	if _, err := io.ReadFull(r, fr.Payload); err != nil {
		return nil, err
	}
	if fr.Masked {
		wsMask(fr.Payload, fr.MaskKey)
	}
	return fr, nil
}

// encode serializes the frame to wire bytes. If Masked, the payload is re-masked
// with MaskKey — reusing the original key reproduces the exact ciphertext for an
// unchanged payload and stays valid for an edited one.
func (fr *wsFrame) encode() []byte {
	n := len(fr.Payload)
	b0 := fr.Opcode
	if fr.Fin {
		b0 |= 0x80
	}
	out := []byte{b0}

	maskBit := byte(0)
	if fr.Masked {
		maskBit = 0x80
	}
	switch {
	case n < 126:
		out = append(out, maskBit|byte(n))
	case n < 65536:
		out = append(out, maskBit|126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		out = append(out, ext[:]...)
	default:
		out = append(out, maskBit|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		out = append(out, ext[:]...)
	}

	if fr.Masked {
		out = append(out, fr.MaskKey[:]...)
		masked := make([]byte, n)
		copy(masked, fr.Payload)
		wsMask(masked, fr.MaskKey)
		return append(out, masked...)
	}
	return append(out, fr.Payload...)
}

// wsMask applies (or reverses — it is its own inverse) the RFC 6455 masking.
func wsMask(p []byte, key [4]byte) {
	for i := range p {
		p[i] ^= key[i%4]
	}
}

func isWSData(op byte) bool {
	return op == opContinuation || op == opText || op == opBinary
}

func wsOpcodeName(op byte) string {
	switch op {
	case opContinuation:
		return "continuation"
	case opText:
		return "text"
	case opBinary:
		return "binary"
	case opClose:
		return "close"
	case opPing:
		return "ping"
	case opPong:
		return "pong"
	default:
		return "?"
	}
}
