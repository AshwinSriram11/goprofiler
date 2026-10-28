package pprofwire

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Protobuf wire types. A field on the wire is a varint key,
// (fieldNumber<<3 | wireType), followed by a value whose layout depends on
// the wire type.
const (
	wireVarint  = 0 // int32/int64/uint64/bool/enum
	wireFixed64 = 1
	wireBytes   = 2 // strings, embedded messages, packed repeated scalars
	wireFixed32 = 5
)

var errTruncated = errors.New("truncated or malformed varint/length")

// field is one decoded key/value pair from a message.
type field struct {
	num  int
	wire int
	v    uint64 // varint or fixed value
	data []byte // payload for wireBytes
}

// bytes returns the payload of a length-delimited field, for fields that
// should hold a string or an embedded message.
func (f field) bytes() ([]byte, error) {
	if f.wire != wireBytes {
		return nil, fmt.Errorf("field %d: want wire type %d (bytes), got %d", f.num, wireBytes, f.wire)
	}
	return f.data, nil
}

// varint returns the value of a varint-typed field.
func (f field) varint() (uint64, error) {
	if f.wire != wireVarint {
		return 0, fmt.Errorf("field %d: want wire type %d (varint), got %d", f.num, wireVarint, f.wire)
	}
	return f.v, nil
}

func setInt(dst *int64, f field) error {
	v, err := f.varint()
	*dst = int64(v) // negative int64s arrive as 10-byte two's-complement varints
	return err
}

func setUint(dst *uint64, f field) error {
	v, err := f.varint()
	*dst = v
	return err
}

func setBool(dst *bool, f field) error {
	v, err := f.varint()
	*dst = v != 0
	return err
}

// eachField walks the fields of one encoded message in order, calling fn for
// each. Fields the caller doesn't care about are still handed to fn, so
// callers can simply ignore field numbers they don't recognise.
func eachField(b []byte, fn func(field) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return errTruncated
		}
		b = b[n:]

		f := field{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case wireVarint:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return errTruncated
			}
			f.v, b = v, b[n:]
		case wireFixed64:
			if len(b) < 8 {
				return errTruncated
			}
			f.v, b = binary.LittleEndian.Uint64(b), b[8:]
		case wireBytes:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return errTruncated
			}
			f.data, b = b[n:n+int(l)], b[n+int(l):]
		case wireFixed32:
			if len(b) < 4 {
				return errTruncated
			}
			f.v, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return fmt.Errorf("field %d: unsupported wire type %d", f.num, f.wire)
		}

		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

// appendVarints appends the integer(s) carried by f to dst. A repeated scalar
// field can arrive packed (one length-delimited blob of back-to-back varints)
// or unpacked (one varint-typed field per element); decoders must accept both.
func appendVarints(dst []uint64, f field) ([]uint64, error) {
	switch f.wire {
	case wireVarint:
		return append(dst, f.v), nil
	case wireBytes:
		b := f.data
		for len(b) > 0 {
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errTruncated
			}
			dst, b = append(dst, v), b[n:]
		}
		return dst, nil
	default:
		return nil, fmt.Errorf("field %d: want varint or packed bytes, got wire type %d", f.num, f.wire)
	}
}
