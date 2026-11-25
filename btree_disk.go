package main

import (
	"encoding"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
)

const (
	metaPageType     = 0
	internalPageType = 1
	leafPageType     = 2
	pageHeaderSize   = 9
	diskPageSize     = 4096
)

type PageHeader struct {
	pageType uint8
	nextPage uint64
}

func (h PageHeader) appendBinary(dst []byte) []byte {
	dst = append(dst, h.pageType)
	return binary.BigEndian.AppendUint64(dst, h.nextPage)
}

func readPageHeader(src []byte, wantType uint8) (PageHeader, error) {
	if len(src) < pageHeaderSize {
		return PageHeader{}, io.ErrUnexpectedEOF
	}
	if src[0] != wantType {
		return PageHeader{}, fmt.Errorf("page type %d, want %d", src[0], wantType)
	}
	return PageHeader{pageType: src[0], nextPage: binary.BigEndian.Uint64(src[1:])}, nil
}

type MetaPage struct {
	header PageHeader
}

func (page MetaPage) MarshalBinary() ([]byte, error) {
	if page.header.pageType != metaPageType {
		return nil, fmt.Errorf("invalid meta page type %d", page.header.pageType)
	}
	return page.header.appendBinary(make([]byte, 0, pageHeaderSize)), nil
}

func (page *MetaPage) UnmarshalBinary(src []byte) error {
	header, err := readPageHeader(src, metaPageType)
	if err != nil {
		return err
	}
	page.header = header
	return nil
}

type BPTreeDisk struct {
	fileName string
	mu       sync.RWMutex
}

// NewBPTreeDisk creates a handle for a file. Use one writer handle per file.
func NewBPTreeDisk(fileName string) *BPTreeDisk {
	return &BPTreeDisk{fileName: fileName}
}

// Insert adds a key-value pair, retaining duplicate keys in insertion order.
func (tree *BPTreeDisk) Insert(key, value int64) error {
	_, err := tree.change(NewKeyValFromInt(key, value), insertRecord)
	return err
}

// Set inserts a key-value pair or replaces the most recent record for that key.
func (tree *BPTreeDisk) Set(key, value []byte) error {
	encodedKey, err := NewKeyEntryFromBytes(key)
	if err != nil {
		return err
	}
	entry, err := newKeyValFromBytes(encodedKey, value)
	if err != nil {
		return err
	}
	_, err = tree.change(entry, setRecord)
	return err
}

// Del removes the most recent record for key and reports whether it existed.
func (tree *BPTreeDisk) Del(key []byte) (bool, error) {
	encodedKey, err := NewKeyEntryFromBytes(key)
	if err != nil {
		return false, err
	}
	return tree.change(KeyVal{key: encodedKey}, deleteRecord)
}

// Find returns the most recent record for key.
func (tree *BPTreeDisk) Find(key []byte) (KeyVal, bool, error) {
	encodedKey, err := NewKeyEntryFromBytes(key)
	if err != nil {
		return KeyVal{}, false, err
	}
	tree.mu.RLock()
	defer tree.mu.RUnlock()
	file, err := os.Open(tree.fileName)
	if errors.Is(err, os.ErrNotExist) {
		return KeyVal{}, false, nil
	}
	if err != nil {
		return KeyVal{}, false, err
	}
	defer file.Close()

	meta, _, err := loadDiskMeta(file)
	offset := meta.header.nextPage
	if err != nil || offset == 0 {
		return KeyVal{}, false, err
	}
	block, err := readDiskPage(file, offset)
	if err != nil {
		return KeyVal{}, false, err
	}
	for {
		page, err := decodeInternalPage(block[:], offset)
		if err != nil {
			return KeyVal{}, false, err
		}
		pos := page.FindLastLE(encodedKey)
		if pos < 0 {
			return KeyVal{}, false, nil
		}
		child := page.children[pos]
		block, err = readDiskChild(file, offset, child)
		if err != nil {
			return KeyVal{}, false, err
		}
		if block[0] == leafPageType {
			leaf, err := decodeLeafPage(block[:], child)
			if err != nil {
				return KeyVal{}, false, err
			}
			pos := leaf.FindLastLE(encodedKey)
			if pos >= 0 && leaf.kv[pos].key == encodedKey {
				return leaf.kv[pos], true, nil
			}
			return KeyVal{}, false, nil
		}
		offset = child
	}
}

type recordChange uint8

const (
	insertRecord recordChange = iota
	setRecord
	deleteRecord
)

func (tree *BPTreeDisk) change(entry KeyVal, change recordChange) (bool, error) {
	tree.mu.Lock()
	defer tree.mu.Unlock()

	flags := os.O_RDWR
	if change != deleteRecord {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(tree.fileName, flags, 0o644)
	if change == deleteRecord && errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()

	meta, size, err := loadDiskMeta(file)
	if err != nil {
		return false, err
	}
	if meta.header.nextPage == 0 && change == deleteRecord {
		return false, nil
	}
	if size == 0 {
		if _, err := file.WriteAt(make([]byte, diskPageSize), 0); err != nil {
			return false, fmt.Errorf("initialize meta page: %w", err)
		}
		size = diskPageSize
	}

	// Pages are appended so a failed write to the new path leaves the existing path intact.
	// Round up past any partial page left by a failed append.
	nextOffset := (size + diskPageSize - 1) / diskPageSize * diskPageSize
	writer := diskPageWriter{file: file, nextOffset: nextOffset}
	root, changed, err := writer.changeRoot(meta.header.nextPage, entry, change)
	if err != nil || !changed {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("sync new pages: %w", err)
	}
	meta.header.nextPage = root
	encoded := meta.header.appendBinary(nil)
	if _, err := file.WriteAt(encoded, 0); err != nil {
		return false, fmt.Errorf("update root offset: %w", err)
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("sync root offset: %w", err)
	}
	return true, nil
}

func loadDiskMeta(file *os.File) (MetaPage, uint64, error) {
	var meta MetaPage
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return meta, 0, err
	}
	block, err := readDiskPage(file, 0)
	if err != nil {
		return meta, 0, err
	}
	if err := meta.UnmarshalBinary(block[:]); err != nil {
		return meta, 0, fmt.Errorf("decode meta page: %w", err)
	}
	return meta, uint64(info.Size()), nil
}

func readDiskPage(file *os.File, offset uint64) ([diskPageSize]byte, error) {
	var block [diskPageSize]byte
	if offset%diskPageSize != 0 || offset > math.MaxInt64-diskPageSize {
		return block, fmt.Errorf("invalid page offset %d", offset)
	}
	if _, err := file.ReadAt(block[:], int64(offset)); err != nil {
		return block, fmt.Errorf("read page at %d: %w", offset, err)
	}
	return block, nil
}

func readDiskChild(file *os.File, parent, child uint64) ([diskPageSize]byte, error) {
	if child == 0 || child >= parent {
		return [diskPageSize]byte{}, fmt.Errorf("invalid child offset %d in page %d", child, parent)
	}
	return readDiskPage(file, child)
}

type diskPageWriter struct {
	file       *os.File
	nextOffset uint64
}

type pageLink struct {
	key    KeyEntry
	offset uint64
}

type pageChange struct {
	left, right pageLink
	changed     bool
}

func (writer *diskPageWriter) appendPage(page encoding.BinaryMarshaler, firstKey KeyEntry) (pageLink, error) {
	encoded, err := page.MarshalBinary()
	if err != nil {
		return pageLink{}, err
	}
	if len(encoded) > diskPageSize || writer.nextOffset > math.MaxInt64-diskPageSize {
		return pageLink{}, fmt.Errorf("cannot append %d-byte page at offset %d", len(encoded), writer.nextOffset)
	}
	var block [diskPageSize]byte
	copy(block[:], encoded)
	offset := writer.nextOffset
	if _, err := writer.file.WriteAt(block[:], int64(offset)); err != nil {
		return pageLink{}, fmt.Errorf("write page at %d: %w", offset, err)
	}
	writer.nextOffset += diskPageSize
	return pageLink{key: firstKey, offset: offset}, nil
}

func (writer *diskPageWriter) changeRoot(rootOffset uint64, entry KeyVal, change recordChange) (uint64, bool, error) {
	if rootOffset == 0 {
		leaf := NewLPage()
		leaf.InsertKV(entry)
		child, err := writer.appendPage(leaf, entry.key)
		if err != nil {
			return 0, false, err
		}
		root := NewIPage()
		root.InsertKV(child.key, child.offset)
		link, err := writer.appendPage(root, child.key)
		return link.offset, err == nil, err
	}

	block, err := readDiskPage(writer.file, rootOffset)
	if err != nil {
		return 0, false, err
	}
	result, err := writer.changeInternal(rootOffset, block[:], entry, change)
	if err != nil || result.right.offset == 0 {
		return result.left.offset, result.changed, err
	}
	root := NewIPage()
	root.InsertKV(result.left.key, result.left.offset)
	root.InsertKV(result.right.key, result.right.offset)
	link, err := writer.appendPage(root, result.left.key)
	return link.offset, err == nil, err
}

func decodeInternalPage(block []byte, offset uint64) (BTreeInternalPage, error) {
	var page BTreeInternalPage
	if err := page.UnmarshalBinary(block); err != nil {
		return BTreeInternalPage{}, fmt.Errorf("decode internal page at %d: %w", offset, err)
	}
	if page.nkey == 0 || int(page.nkey) >= len(page.keys) {
		return BTreeInternalPage{}, fmt.Errorf("invalid child count %d at offset %d", page.nkey, offset)
	}
	return page, nil
}

func (writer *diskPageWriter) changeInternal(offset uint64, block []byte, entry KeyVal, change recordChange) (pageChange, error) {
	page, err := decodeInternalPage(block, offset)
	if err != nil {
		return pageChange{}, err
	}
	pos := page.FindLastLE(entry.key)
	if pos < 0 && change == deleteRecord {
		return pageChange{}, nil
	}
	pos = max(0, pos)
	child := page.children[pos]
	childBlock, err := readDiskChild(writer.file, offset, child)
	if err != nil {
		return pageChange{}, err
	}
	var result pageChange
	switch childBlock[0] {
	case internalPageType:
		result, err = writer.changeInternal(child, childBlock[:], entry, change)
	case leafPageType:
		result, err = writer.changeLeaf(child, childBlock[:], entry, change)
	default:
		return pageChange{}, fmt.Errorf("unexpected page type %d at offset %d", childBlock[0], child)
	}
	if err != nil || !result.changed {
		return pageChange{}, err
	}
	if result.left.offset == 0 {
		page.deleteAt(pos)
		if page.nkey == 0 {
			return pageChange{changed: true}, nil
		}
	} else {
		page.keys[pos], page.children[pos] = result.left.key, result.left.offset
		if result.right.offset != 0 {
			page.insertAt(pos+1, result.right.key, result.right.offset)
		}
	}
	updated := pageChange{changed: true}
	if int(page.nkey) == len(page.keys) {
		sibling := page.Split()
		updated.right, err = writer.appendPage(sibling, sibling.keys[0])
		if err != nil {
			return pageChange{}, err
		}
	}
	updated.left, err = writer.appendPage(page, page.keys[0])
	return updated, err
}

func decodeLeafPage(block []byte, offset uint64) (BTreeLeafPage, error) {
	var page BTreeLeafPage
	if err := page.UnmarshalBinary(block); err != nil {
		return BTreeLeafPage{}, fmt.Errorf("decode leaf page at %d: %w", offset, err)
	}
	if page.nkv == 0 || int(page.nkv) >= len(page.kv) {
		return BTreeLeafPage{}, fmt.Errorf("invalid record count %d at offset %d", page.nkv, offset)
	}
	return page, nil
}

func (writer *diskPageWriter) changeLeaf(offset uint64, block []byte, entry KeyVal, change recordChange) (pageChange, error) {
	page, err := decodeLeafPage(block, offset)
	if err != nil {
		return pageChange{}, err
	}
	pos := page.FindLastLE(entry.key)
	found := pos >= 0 && page.kv[pos].key == entry.key
	switch {
	case change == deleteRecord:
		if !found {
			return pageChange{}, nil
		}
		page.deleteAt(pos)
		if page.nkv == 0 {
			return pageChange{changed: true}, nil
		}
	case change == setRecord && found:
		page.kv[pos] = entry
	default:
		page.insertAt(pos+1, entry)
	}
	updated := pageChange{changed: true}
	if int(page.nkv) == len(page.kv) {
		sibling := page.Split()
		updated.right, err = writer.appendPage(sibling, sibling.kv[0].key)
		if err != nil {
			return pageChange{}, err
		}
	}
	updated.left, err = writer.appendPage(page, page.kv[0].key)
	return updated, err
}
