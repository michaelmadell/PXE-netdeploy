package tftp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
)

// Opcodes (RFC 1350, RFC 2347).
const (
	opRRQ   uint16 = 1
	opWRQ   uint16 = 2
	opDATA  uint16 = 3
	opACK   uint16 = 4
	opERROR uint16 = 5
	opOACK  uint16 = 6
)

// Error codes (RFC 1350, RFC 2347).
const (
	errNotDefined      uint16 = 0
	errFileNotFound    uint16 = 1
	errAccessViolation uint16 = 2
	errIllegalOp       uint16 = 4
	errOptionsRefused  uint16 = 8
)

// request is a parsed RRQ or WRQ.
type request struct {
	op       uint16
	filename string
	mode     string
	// options holds requested options with lower-cased names, in the order
	// the client sent them. Values are kept as sent.
	options []option
}

type option struct{ name, value string }

var errMalformed = errors.New("malformed request")

// parseRequest decodes an RRQ/WRQ: opcode, then NUL-terminated filename,
// mode and option name/value pairs.
func parseRequest(b []byte) (request, error) {
	if len(b) < 4 {
		return request{}, errMalformed
	}
	r := request{op: binary.BigEndian.Uint16(b)}
	if r.op != opRRQ && r.op != opWRQ {
		return request{}, errMalformed
	}
	fields := bytes.Split(b[2:], []byte{0})
	// A well-formed packet ends with NUL, so the last field is empty.
	if len(fields) < 3 || len(fields[len(fields)-1]) != 0 {
		return request{}, errMalformed
	}
	fields = fields[:len(fields)-1]
	r.filename = string(fields[0])
	r.mode = strings.ToLower(string(fields[1]))
	if r.filename == "" {
		return request{}, errMalformed
	}
	rest := fields[2:]
	// An odd trailing field is a name without a value; ignore it.
	for i := 0; i+1 < len(rest); i += 2 {
		r.options = append(r.options, option{
			name:  strings.ToLower(string(rest[i])),
			value: string(rest[i+1]),
		})
	}
	return r, nil
}

func dataPacket(buf []byte, block uint16, n int) []byte {
	binary.BigEndian.PutUint16(buf[0:], opDATA)
	binary.BigEndian.PutUint16(buf[2:], block)
	return buf[:4+n]
}

func oackPacket(opts []option) []byte {
	b := []byte{0, byte(opOACK)}
	for _, o := range opts {
		b = append(b, o.name...)
		b = append(b, 0)
		b = append(b, o.value...)
		b = append(b, 0)
	}
	return b
}

func errorPacket(code uint16, msg string) []byte {
	b := make([]byte, 4, 5+len(msg))
	binary.BigEndian.PutUint16(b[0:], opERROR)
	binary.BigEndian.PutUint16(b[2:], code)
	b = append(b, msg...)
	return append(b, 0)
}

// parseError returns the code and message of an ERROR packet.
func parseError(b []byte) (uint16, string) {
	if len(b) < 4 {
		return 0, ""
	}
	msg := b[4:]
	if i := bytes.IndexByte(msg, 0); i >= 0 {
		msg = msg[:i]
	}
	return binary.BigEndian.Uint16(b[2:]), string(msg)
}
