package main

import (
	"encoding"
	"encoding/binary"
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
	mu       sync.Mutex
}

// NewBPTreeDisk creates a handle for a file. Use one writer handle per file.
func NewBPTreeDisk(fileName string) *BPTreeDisk {
	return &BPTreeDisk{fileName: fileName}
}

// Insert adds a key-value pair, retaining duplicate keys in insertion order.
func (tree *BPTreeDisk) Insert(key, value int64) error {
	tree.mu.Lock()
	defer tree.mu.Unlock()

	file, err := os.OpenFile(tree.fileName, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	var meta MetaPage
	if info.Size() == 0 {
		if _, err := file.WriteAt(make([]byte, diskPageSize), 0); err != nil {
			return fmt.Errorf("initialize meta page: %w", err)
		}
	} else {
		block, err := readDiskPage(file, 0)
		if err != nil {
			return err
		}
		if err := meta.UnmarshalBinary(block[:]); err != nil {
			return fmt.Errorf("decode meta page: %w", err)
		}
	}

	// Pages are appended so a failed write to the new path leaves the existing path intact.
	// Round up past any partial page left by a failed append.
	size := max(uint64(info.Size()), uint64(diskPageSize))
	nextOffset := (size + diskPageSize - 1) / diskPageSize * diskPageSize
	writer := diskPageWriter{file: file, nextOffset: nextOffset}
	root, err := writer.insert(meta.header.nextPage, uint64(info.Size()), NewKeyValFromInt(key, value))
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync new pages: %w", err)
	}
	meta.header.nextPage = root
	encoded := meta.header.appendBinary(nil)
	if _, err := file.WriteAt(encoded, 0); err != nil {
		return fmt.Errorf("update root offset: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync root offset: %w", err)
	}
	return nil
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

type diskPageWriter struct {
	file       *os.File
	nextOffset uint64
}

type pageLink struct {
	key    KeyEntry
	offset uint64
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

func (writer *diskPageWriter) insert(rootOffset, fileSize uint64, entry KeyVal) (uint64, error) {
	if rootOffset == 0 {
		leaf := NewLPage()
		leaf.InsertKV(entry)
		child, err := writer.appendPage(leaf, entry.key)
		if err != nil {
			return 0, err
		}
		root := NewIPage()
		root.InsertKV(child.key, child.offset)
		link, err := writer.appendPage(root, child.key)
		return link.offset, err
	}

	if rootOffset >= fileSize {
		return 0, fmt.Errorf("root page offset %d is outside the file", rootOffset)
	}
	block, err := readDiskPage(writer.file, rootOffset)
	if err != nil {
		return 0, err
	}
	left, right, err := writer.insertInternal(rootOffset, block[:], entry)
	if err != nil || right.offset == 0 {
		return left.offset, err
	}
	root := NewIPage()
	root.InsertKV(left.key, left.offset)
	root.InsertKV(right.key, right.offset)
	link, err := writer.appendPage(root, left.key)
	return link.offset, err
}

func (writer *diskPageWriter) insertInternal(offset uint64, block []byte, entry KeyVal) (left, right pageLink, err error) {
	var page BTreeInternalPage
	if err := page.UnmarshalBinary(block); err != nil {
		return pageLink{}, pageLink{}, fmt.Errorf("decode internal page at %d: %w", offset, err)
	}
	if page.nkey == 0 || int(page.nkey) >= len(page.keys) {
		return pageLink{}, pageLink{}, fmt.Errorf("invalid child count %d at offset %d", page.nkey, offset)
	}
	pos := max(0, page.FindLastLE(entry.key))
	child := page.children[pos]
	if child == 0 || child >= offset {
		return pageLink{}, pageLink{}, fmt.Errorf("invalid child offset %d in page %d", child, offset)
	}
	childBlock, err := readDiskPage(writer.file, child)
	if err != nil {
		return pageLink{}, pageLink{}, err
	}
	var childLeft, childRight pageLink
	switch childBlock[0] {
	case internalPageType:
		childLeft, childRight, err = writer.insertInternal(child, childBlock[:], entry)
	case leafPageType:
		childLeft, childRight, err = writer.insertLeaf(child, childBlock[:], entry)
	default:
		return pageLink{}, pageLink{}, fmt.Errorf("unexpected page type %d at offset %d", childBlock[0], child)
	}
	if err != nil {
		return pageLink{}, pageLink{}, err
	}
	page.keys[pos], page.children[pos] = childLeft.key, childLeft.offset
	if childRight.offset != 0 {
		page.insertAt(pos+1, childRight.key, childRight.offset)
	}
	if int(page.nkey) == len(page.keys) {
		sibling := page.Split()
		right, err = writer.appendPage(sibling, sibling.keys[0])
		if err != nil {
			return pageLink{}, pageLink{}, err
		}
	}
	left, err = writer.appendPage(page, page.keys[0])
	return left, right, err
}

func (writer *diskPageWriter) insertLeaf(offset uint64, block []byte, entry KeyVal) (left, right pageLink, err error) {
	var page BTreeLeafPage
	if err := page.UnmarshalBinary(block); err != nil {
		return pageLink{}, pageLink{}, fmt.Errorf("decode leaf page at %d: %w", offset, err)
	}
	if page.nkv == 0 || int(page.nkv) >= len(page.kv) {
		return pageLink{}, pageLink{}, fmt.Errorf("invalid record count %d at offset %d", page.nkv, offset)
	}
	page.InsertKV(entry)
	if int(page.nkv) == len(page.kv) {
		sibling := page.Split()
		right, err = writer.appendPage(sibling, sibling.kv[0].key)
		if err != nil {
			return pageLink{}, pageLink{}, err
		}
	}
	left, err = writer.appendPage(page, page.kv[0].key)
	return left, right, err
}
