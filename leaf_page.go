package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

const (
	maxValueSize   = 32
	intValueSize   = 8
	leafRecordSize = 2*uint16Size + intKeySize + maxValueSize
	leafMaxKV      = (diskPageSize - pageHeaderSize - uint16Size) / leafRecordSize
)

type KeyVal struct {
	key         KeyEntry
	valueLength uint16
	value       [maxValueSize]byte
}

func NewKeyValFromInt(key, value int64) KeyVal {
	entry := KeyVal{key: NewKeyEntryFromInt(key), valueLength: intValueSize}
	binary.BigEndian.PutUint64(entry.value[maxValueSize-intValueSize:], uint64(value))
	return entry
}

func newKeyValFromBytes(key KeyEntry, value []byte) (KeyVal, error) {
	if len(value) > maxValueSize {
		return KeyVal{}, fmt.Errorf("value length %d exceeds %d bytes", len(value), maxValueSize)
	}
	entry := KeyVal{key: key, valueLength: uint16(len(value))}
	copy(entry.value[maxValueSize-len(value):], value)
	return entry, nil
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
	return sort.Search(int(page.nkv), func(i int) bool {
		return page.kv[i].key.compare(key) > 0
	}) - 1
}

func (page *BTreeLeafPage) InsertKV(entry KeyVal) {
	page.insertAt(page.FindLastLE(entry.key)+1, entry)
}

func (page *BTreeLeafPage) insertAt(pos int, entry KeyVal) {
	if int(page.nkv) == len(page.kv) {
		panic("cannot insert into a full leaf page")
	}
	copy(page.kv[pos+1:], page.kv[pos:page.nkv])
	page.kv[pos] = entry
	page.nkv++
}

func (page *BTreeLeafPage) deleteAt(pos int) {
	copy(page.kv[pos:], page.kv[pos+1:page.nkv])
	page.nkv--
	page.kv[page.nkv] = KeyVal{}
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

func (page *BTreeLeafPage) binarySize() int {
	size := pageHeaderSize + uint16Size
	for _, entry := range page.kv[:page.nkv] {
		size += 2*uint16Size + int(entry.key.length) + int(entry.valueLength)
	}
	return size
}

// MarshalBinary encodes the header, record count, and records with separate key and value lengths.
func (page BTreeLeafPage) MarshalBinary() ([]byte, error) {
	if page.header.pageType != leafPageType || int(page.nkv) > len(page.kv) {
		return nil, fmt.Errorf("invalid leaf page type %d or record count %d", page.header.pageType, page.nkv)
	}
	size := page.binarySize()
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
