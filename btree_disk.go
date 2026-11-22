package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	metaPageType     = 0
	internalPageType = 1
	leafPageType     = 2
	pageHeaderSize   = 9
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
	head MetaPage
}

func NewBPTreeDisk() BPTreeDisk {
	return BPTreeDisk{}
}
