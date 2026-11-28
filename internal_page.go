package main

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

const (
	maxKeySize          = 16
	intKeySize          = 8
	uint16Size          = 2
	pagePtrSize         = 8
	internalRecordSize  = uint16Size + intKeySize + pagePtrSize
	internalPageMaxKeys = (diskPageSize - pageHeaderSize - uint16Size) / internalRecordSize
)

type KeyEntry struct {
	length uint16
	data   [maxKeySize]byte // Big-endian, with shorter keys right-aligned.
}

func NewKeyEntryFromInt(key int64) KeyEntry {
	entry := KeyEntry{length: intKeySize}
	binary.BigEndian.PutUint64(entry.data[maxKeySize-intKeySize:], uint64(key))
	return entry
}

func NewKeyEntryFromBytes(data []byte) (KeyEntry, error) {
	if len(data) > maxKeySize {
		return KeyEntry{}, fmt.Errorf("key length %d exceeds %d bytes", len(data), maxKeySize)
	}
	key := KeyEntry{length: uint16(len(data))}
	copy(key.data[maxKeySize-len(data):], data)
	return key, nil
}

func (key KeyEntry) compare(other KeyEntry) int {
	keyLong, otherLong := key.length > intKeySize, other.length > intKeySize
	if keyLong != otherLong {
		if keyLong {
			return 1
		}
		return -1
	}
	if keyLong {
		return bytes.Compare(key.data[maxKeySize-key.length:], other.data[maxKeySize-other.length:])
	}
	// Preserve the order of existing integer keys and distinguish leading zero bytes.
	left := int64(binary.BigEndian.Uint64(key.data[maxKeySize-intKeySize:]))
	right := int64(binary.BigEndian.Uint64(other.data[maxKeySize-intKeySize:]))
	if order := cmp.Compare(left, right); order != 0 {
		return order
	}
	return cmp.Compare(key.length, other.length)
}

func readKeyEntry(src []byte) (KeyEntry, []byte, error) {
	if len(src) < uint16Size {
		return KeyEntry{}, nil, io.ErrUnexpectedEOF
	}
	length := binary.BigEndian.Uint16(src)
	if length > maxKeySize {
		return KeyEntry{}, nil, fmt.Errorf("key length %d exceeds %d bytes", length, maxKeySize)
	}
	src = src[uint16Size:]
	if len(src) < int(length) {
		return KeyEntry{}, nil, io.ErrUnexpectedEOF
	}
	entry := KeyEntry{length: length}
	copy(entry.data[maxKeySize-length:], src[:length])
	return entry, src[length:], nil
}

type BTreeInternalPage struct {
	header   PageHeader
	nkey     uint16
	keys     [internalPageMaxKeys]KeyEntry
	children [internalPageMaxKeys]uint64
}

func NewIPage() BTreeInternalPage {
	return BTreeInternalPage{header: PageHeader{pageType: internalPageType}}
}

func (page *BTreeInternalPage) FindLastLE(findKey KeyEntry) int {
	return sort.Search(int(page.nkey), func(i int) bool {
		return page.keys[i].compare(findKey) > 0
	}) - 1
}

// InsertKV inserts a key and its child page offset. The page must have room.
func (page *BTreeInternalPage) InsertKV(key KeyEntry, child uint64) {
	page.insertAt(page.FindLastLE(key)+1, key, child)
}

func (page *BTreeInternalPage) insertAt(pos int, key KeyEntry, child uint64) {
	if int(page.nkey) == len(page.keys) {
		panic("cannot insert into a full internal page")
	}
	for i := int(page.nkey); i > pos; i-- {
		page.keys[i] = page.keys[i-1]
		page.children[i] = page.children[i-1]
	}
	page.keys[pos] = key
	page.children[pos] = child
	page.nkey++
}

func (page *BTreeInternalPage) deleteAt(pos int) {
	copy(page.keys[pos:], page.keys[pos+1:page.nkey])
	copy(page.children[pos:], page.children[pos+1:page.nkey])
	page.nkey--
	page.keys[page.nkey] = KeyEntry{}
	page.children[page.nkey] = 0
}

// Split keeps the left half in page and returns a new internal page for the right half.
func (page *BTreeInternalPage) Split() BTreeInternalPage {
	mid := page.nkey / 2
	keys := page.keys[mid:page.nkey]
	children := page.children[mid:page.nkey]

	right := NewIPage()
	right.nkey = page.nkey - mid
	copy(right.keys[:], keys)
	copy(right.children[:], children)
	clear(keys)
	clear(children)
	page.nkey = mid
	return right
}

func (page *BTreeInternalPage) binarySize() int {
	size := pageHeaderSize + uint16Size
	for _, key := range page.keys[:page.nkey] {
		size += uint16Size + int(key.length) + pagePtrSize
	}
	return size
}

// MarshalBinary encodes the header, key count, length-prefixed keys, and child offsets.
func (page BTreeInternalPage) MarshalBinary() ([]byte, error) {
	if page.header.pageType != internalPageType {
		return nil, fmt.Errorf("invalid internal page type %d", page.header.pageType)
	}
	if int(page.nkey) > len(page.keys) {
		return nil, fmt.Errorf("internal page key count %d exceeds %d", page.nkey, len(page.keys))
	}
	size := page.binarySize()
	dst := page.header.appendBinary(make([]byte, 0, size))
	dst = binary.BigEndian.AppendUint16(dst, page.nkey)
	for _, key := range page.keys[:page.nkey] {
		if key.length > maxKeySize {
			return nil, fmt.Errorf("key length %d exceeds %d bytes", key.length, maxKeySize)
		}
		dst = binary.BigEndian.AppendUint16(dst, key.length)
		dst = append(dst, key.data[maxKeySize-key.length:]...)
	}
	for _, child := range page.children[:page.nkey] {
		dst = binary.BigEndian.AppendUint64(dst, child)
	}
	return dst, nil
}

// UnmarshalBinary decodes the page at the start of src, which may include padding.
func (page *BTreeInternalPage) UnmarshalBinary(src []byte) error {
	header, err := readPageHeader(src, internalPageType)
	if err != nil {
		return err
	}
	src = src[pageHeaderSize:]
	if len(src) < uint16Size {
		return io.ErrUnexpectedEOF
	}
	decoded := BTreeInternalPage{header: header, nkey: binary.BigEndian.Uint16(src)}
	if int(decoded.nkey) > len(decoded.keys) {
		return fmt.Errorf("internal page key count %d exceeds %d", decoded.nkey, len(decoded.keys))
	}
	src = src[uint16Size:]
	for i := range decoded.nkey {
		decoded.keys[i], src, err = readKeyEntry(src)
		if err != nil {
			return fmt.Errorf("decode key %d: %w", i, err)
		}
	}
	if len(src) < int(decoded.nkey)*pagePtrSize {
		return io.ErrUnexpectedEOF
	}
	for i := range decoded.nkey {
		decoded.children[i] = binary.BigEndian.Uint64(src)
		src = src[pagePtrSize:]
	}
	*page = decoded
	return nil
}
