package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	leafMaxKV    = 4
	maxValueSize = 8
)

type KeyVal struct {
	key         KeyEntry
	valueLength uint16
	value       [maxValueSize]byte
}

func NewKeyValFromInt(key, value int64) KeyVal {
	entry := KeyVal{key: NewKeyEntryFromInt(key), valueLength: maxValueSize}
	binary.BigEndian.PutUint64(entry.value[:], uint64(value))
	return entry
}

type BTreeLeafPage struct {
	header PageHeader
	nkv    uint16
	kv     [leafMaxKV]KeyVal
}

func NewLPage() BTreeLeafPage {
	return BTreeLeafPage{header: PageHeader{pageType: leafPageType}}
}

func (page *BTreeLeafPage) FindLastLE(key KeyEntry) int {
	for i := int(page.nkv) - 1; i >= 0; i-- {
		if page.kv[i].key.compare(key) <= 0 {
			return i
		}
	}
	return -1
}

func (page *BTreeLeafPage) InsertKV(entry KeyVal) {
	if int(page.nkv) == len(page.kv) {
		panic("cannot insert into a full leaf page")
	}
	pos := page.FindLastLE(entry.key) + 1
	copy(page.kv[pos+1:], page.kv[pos:page.nkv])
	page.kv[pos] = entry
	page.nkv++
}

func (page *BTreeLeafPage) Split() BTreeLeafPage {
	mid := page.nkv / 2
	entries := page.kv[mid:page.nkv]
	right := NewLPage()
	right.nkv = uint16(copy(right.kv[:], entries))
	clear(entries)
	page.nkv = mid
	return right
}

// MarshalBinary encodes the header, record count, and records with separate key and value lengths.
func (page BTreeLeafPage) MarshalBinary() ([]byte, error) {
	if page.header.pageType != leafPageType || int(page.nkv) > len(page.kv) {
		return nil, fmt.Errorf("invalid leaf page type %d or record count %d", page.header.pageType, page.nkv)
	}
	size := pageHeaderSize + uint16Size + int(page.nkv)*(2*uint16Size+maxKeySize+maxValueSize)
	dst := page.header.appendBinary(make([]byte, 0, size))
	dst = binary.BigEndian.AppendUint16(dst, page.nkv)
	for _, entry := range page.kv[:page.nkv] {
		if entry.key.length > maxKeySize || entry.valueLength > maxValueSize {
			return nil, fmt.Errorf("invalid leaf key or value length (%d, %d)", entry.key.length, entry.valueLength)
		}
		dst = binary.BigEndian.AppendUint16(dst, entry.key.length)
		dst = binary.BigEndian.AppendUint16(dst, entry.valueLength)
		dst = append(dst, entry.key.data[maxKeySize-entry.key.length:]...)
		dst = append(dst, entry.value[maxValueSize-entry.valueLength:]...)
	}
	return dst, nil
}

func (page *BTreeLeafPage) UnmarshalBinary(src []byte) error {
	header, err := readPageHeader(src, leafPageType)
	if err != nil {
		return err
	}
	src = src[pageHeaderSize:]
	if len(src) < uint16Size {
		return io.ErrUnexpectedEOF
	}
	decoded := BTreeLeafPage{header: header, nkv: binary.BigEndian.Uint16(src)}
	if int(decoded.nkv) > len(decoded.kv) {
		return fmt.Errorf("leaf record count %d exceeds %d", decoded.nkv, len(decoded.kv))
	}
	src = src[uint16Size:]
	for i := range decoded.nkv {
		if len(src) < 2*uint16Size {
			return io.ErrUnexpectedEOF
		}
		keyLength := binary.BigEndian.Uint16(src)
		valueLength := binary.BigEndian.Uint16(src[uint16Size:])
		if keyLength > maxKeySize || valueLength > maxValueSize {
			return fmt.Errorf("invalid leaf key or value length (%d, %d)", keyLength, valueLength)
		}
		src = src[2*uint16Size:]
		end := keyLength + valueLength
		if len(src) < int(end) {
			return io.ErrUnexpectedEOF
		}
		entry := &decoded.kv[i]
		entry.key.length = keyLength
		entry.valueLength = valueLength
		copy(entry.key.data[maxKeySize-keyLength:], src[:keyLength])
		copy(entry.value[maxValueSize-valueLength:], src[keyLength:end])
		src = src[end:]
	}
	*page = decoded
	return nil
}
