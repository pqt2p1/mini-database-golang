package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/bits"
	"slices"
)

func orderValues(def TableDef, record Record, count int) ([]Value, error) {
	if len(record.Cols) != len(record.Vals) {
		return nil, fmt.Errorf("record column and value counts differ")
	}
	values := make([]Value, count)
	for i, name := range record.Cols {
		pos := slices.Index(def.Cols[:count], name)
		if pos < 0 {
			return nil, fmt.Errorf("unexpected column %q", name)
		}
		if values[pos].Type != 0 {
			return nil, fmt.Errorf("duplicate column %q", name)
		}
		if record.Vals[i].Type != def.Types[pos] {
			return nil, fmt.Errorf("wrong type for column %q", name)
		}
		values[pos] = record.Vals[i]
	}
	for i, value := range values {
		if value.Type == 0 {
			return nil, fmt.Errorf("missing column %q", def.Cols[i])
		}
	}
	return values, nil
}

func encodeRecordKey(prefix byte, values []Value) ([maxKeySize]byte, error) {
	var key [maxKeySize]byte
	data := key[:1]
	data[0] = prefix
	for _, value := range values {
		if value.Type == TypeInt64 {
			data = appendKeyInt(data, value.I64)
		} else {
			if len(value.Str) > len(key)-len(data)-2 {
				return key, fmt.Errorf("encoded primary key exceeds %d bytes", maxKeySize)
			}
			// Escape NUL and terminate each byte string so tuples remain distinct.
			for _, b := range value.Str {
				data = append(data, b)
				if b == 0 {
					data = append(data, 255)
				}
			}
			data = append(data, 0, 0)
		}
		if len(data) > len(key) {
			return key, fmt.Errorf("encoded primary key exceeds %d bytes", maxKeySize)
		}
	}
	return key, nil
}

// The leading byte orders integers by sign and width. Negative payloads are complemented.
func appendKeyInt(dst []byte, value int64) []byte {
	payload := uint64(value)
	width := (bits.Len64(payload) + 7) / 8
	if value < 0 {
		width = (bits.Len64(^payload) + 7) / 8
		dst = append(dst, byte(8-width))
	} else {
		dst = append(dst, byte(9+width))
	}
	var encoded [intKeySize]byte
	binary.BigEndian.PutUint64(encoded[:], payload)
	return append(dst, encoded[len(encoded)-width:]...)
}

func encodeRowValues(values []Value) ([]byte, error) {
	var data []byte
	for _, value := range values {
		if value.Type == TypeInt64 {
			data = binary.BigEndian.AppendUint64(data, uint64(value.I64))
		} else {
			if len(value.Str) >= maxValueSize {
				return nil, fmt.Errorf("encoded row exceeds %d bytes", maxValueSize)
			}
			data = append(data, byte(len(value.Str)))
			data = append(data, value.Str...)
		}
		if len(data) > maxValueSize {
			return nil, fmt.Errorf("encoded row exceeds %d bytes", maxValueSize)
		}
	}
	return data, nil
}

func decodeRowValues(types []uint8, data []byte) ([]Value, error) {
	values := make([]Value, len(types))
	for i, kind := range types {
		values[i].Type = kind
		if kind == TypeInt64 {
			if len(data) < intValueSize {
				return nil, fmt.Errorf("truncated integer in row")
			}
			values[i].I64 = int64(binary.BigEndian.Uint64(data))
			data = data[intValueSize:]
		} else {
			if len(data) == 0 || int(data[0]) > len(data)-1 {
				return nil, fmt.Errorf("truncated byte string in row")
			}
			end := 1 + int(data[0])
			values[i].Str = bytes.Clone(data[1:end])
			data = data[end:]
		}
	}
	if len(data) != 0 {
		return nil, fmt.Errorf("unexpected bytes after row")
	}
	return values, nil
}
