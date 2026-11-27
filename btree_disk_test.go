package main

import (
	"bytes"
	"encoding/hex"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLeafPageBinaryFormat(t *testing.T) {
	page := NewLPage()
	page.InsertKV(NewKeyValFromInt(-2, 258))
	wire, err := hex.DecodeString("020000000000000000" + "0001" + "00080008" +
		"fffffffffffffffe" + "0000000000000102")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := page.MarshalBinary()
	if err != nil || !bytes.Equal(encoded, wire) {
		t.Fatalf("encoded leaf = %x, want %x (err: %v)", encoded, wire, err)
	}
	var decoded BTreeLeafPage
	if err := decoded.UnmarshalBinary(wire); err != nil || decoded != page {
		t.Fatalf("leaf did not round trip: %v", err)
	}
	for i := range len(wire) {
		if err := decoded.UnmarshalBinary(wire[:i]); err == nil || decoded != page {
			t.Fatalf("truncation at byte %d: err = %v, page changed = %t", i, err, decoded != page)
		}
	}
}

func TestDiskInsertAfterReopenInShuffledOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree.db")
	tree := NewBPTreeDisk(path)
	var want []KeyVal
	insert := func(key, value int64) {
		t.Helper()
		if err := tree.Insert(key, value); err != nil {
			t.Fatalf("insert (%d, %d): %v", key, value, err)
		}
		want = append(want, NewKeyValFromInt(key, value))
	}
	random := rand.New(rand.NewPCG(6, 24))
	for i, key := range random.Perm(180) {
		if i == 90 {
			// Simulate an append that stopped partway through an unreachable page.
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.Write([]byte{1, 2, 3})
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("append partial page: write = %v, close = %v", writeErr, closeErr)
			}
			tree = NewBPTreeDisk(path)
		}
		insert(int64(key)-90, -int64(i)-1)
	}
	for i := range int64(20) {
		insert(0, i)
	}
	insert(-1000, 42)
	slices.SortStableFunc(want, func(a, b KeyVal) int { return a.key.compare(b.key) })

	got, depth := readDiskEntries(t, path)
	if depth != 1 || !slices.Equal(got, want) {
		t.Fatalf("reopened tree has depth %d and %d records, want depth 1 and %d matching records", depth, len(got), len(want))
	}
}

func TestDiskInsertSplitsFullRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree.db")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := diskPageWriter{path: diskPagePath{file: file}, nextOffset: diskPageSize}
	root := NewIPage()
	var want []KeyVal
	for i := range int64(internalPageMaxKeys - 1) {
		leaf := NewLPage()
		count := 1
		if i == 0 {
			count = leafMaxKV - 1
		}
		for j := range count {
			key := i*diskPageSize + int64(j)
			entry := NewKeyValFromInt(key, -key)
			leaf.InsertKV(entry)
			want = append(want, entry)
		}
		link, err := writer.writePage(leaf, leaf.kv[0].key)
		if err != nil {
			t.Fatal(err)
		}
		root.InsertKV(link.key, link.offset)
	}
	link, err := writer.writePage(root, root.keys[0])
	if err != nil {
		t.Fatal(err)
	}
	meta := PageHeader{pageType: metaPageType, nextPage: link.offset}
	if _, err := file.WriteAt(meta.appendBinary(nil), 0); err != nil {
		t.Fatal(err)
	}
	if err := NewBPTreeDisk(path).Insert(-1, 1000); err != nil {
		t.Fatal(err)
	}
	want = append([]KeyVal{NewKeyValFromInt(-1, 1000)}, want...)
	got, depth := readDiskEntries(t, path)
	if depth != 2 || !slices.Equal(got, want) {
		t.Fatalf("tree after root split has depth %d and %d records, want depth 2 and %d matching records", depth, len(got), len(want))
	}
}

func TestDiskPageCapacity(t *testing.T) {
	internal, leaf := NewIPage(), NewLPage()
	maxValue := bytes.Repeat([]byte{255}, maxValueSize)
	for i := range internalPageMaxKeys {
		internal.InsertKV(NewKeyEntryFromInt(int64(i)), diskPageSize)
	}
	for i := range leafMaxKV {
		entry, err := newKeyValFromBytes(NewKeyEntryFromInt(int64(i)), maxValue)
		if err != nil {
			t.Fatal(err)
		}
		leaf.InsertKV(entry)
	}
	data, err := internal.MarshalBinary()
	if err != nil || len(data) > diskPageSize || len(data)+internalRecordSize <= diskPageSize {
		t.Fatalf("full internal page size = %d, err = %v", len(data), err)
	}
	data, err = leaf.MarshalBinary()
	if err != nil || len(data) > diskPageSize || len(data)+leafRecordSize <= diskPageSize {
		t.Fatalf("full leaf page size = %d, err = %v", len(data), err)
	}
}

func TestDiskReusesRetiredPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree.db")
	db := NewKV(path)
	key := []byte("key")
	for i := range byte(20) {
		if err := db.Set(key, []byte{i}); err != nil {
			t.Fatal(err)
		}
		requireKV(t, db, key, []byte{i}, true)
	}
	if deleted, err := db.Del(key); err != nil || !deleted {
		t.Fatalf("delete = %t, %v", deleted, err)
	}
	if err := db.Set(key, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// One meta page and two copies of the leaf and its root suffice for these updates.
	if got, want := info.Size(), int64(5*diskPageSize); got != want {
		t.Fatalf("file size = %d, want %d after page reuse", got, want)
	}
	requireKV(t, NewKV(path), key, []byte("new"), true)
}

func TestDiskRejectsPageCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree.db")
	db := NewKV(path)
	if err := db.Set([]byte("key"), []byte("value")); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	meta, _, err := loadDiskMeta(file)
	if err != nil {
		t.Fatal(err)
	}
	root := NewIPage()
	key, err := NewKeyEntryFromBytes([]byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	root.InsertKV(key, meta.header.nextPage)
	writer := diskPageWriter{path: diskPagePath{file: file}, nextOffset: meta.header.nextPage}
	if _, err := writer.writePage(root, key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Get([]byte("key")); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Get returned %v, want cycle error", err)
	}
	if err := db.Set([]byte("key"), []byte("other")); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Set returned %v, want cycle error", err)
	}
}

func readDiskEntries(t *testing.T, path string) ([]KeyVal, int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	block, err := readDiskPage(file, 0)
	if err != nil {
		t.Fatal(err)
	}
	var meta MetaPage
	if err := meta.UnmarshalBinary(block[:]); err != nil {
		t.Fatal(err)
	}
	var got []KeyVal
	pathReader := diskPagePath{file: file, offsets: make(map[uint64]struct{})}
	_, depth := collectDiskEntries(t, &pathReader, meta.header.nextPage, &got)
	return got, depth
}

func collectDiskEntries(t *testing.T, reader *diskPagePath, offset uint64, entries *[]KeyVal) (KeyEntry, int) {
	t.Helper()
	block, err := reader.read(offset)
	if err != nil {
		t.Fatal(err)
	}
	if block[0] == leafPageType {
		var leaf BTreeLeafPage
		if err := leaf.UnmarshalBinary(block[:]); err != nil || leaf.nkv == 0 || leaf.nkv >= leafMaxKV {
			t.Fatalf("invalid leaf at %d with %d records: %v", offset, leaf.nkv, err)
		}
		*entries = append(*entries, leaf.kv[:leaf.nkv]...)
		return leaf.kv[0].key, 0
	}
	var page BTreeInternalPage
	if err := page.UnmarshalBinary(block[:]); err != nil || page.nkey == 0 || page.nkey >= internalPageMaxKeys {
		t.Fatalf("invalid internal page at %d with %d children: %v", offset, page.nkey, err)
	}
	depth := 0
	for i, child := range page.children[:page.nkey] {
		minKey, childDepth := collectDiskEntries(t, reader, child, entries)
		if page.keys[i].compare(minKey) != 0 {
			t.Fatalf("key %d in page %d does not match its child's minimum", i, offset)
		}
		if i == 0 {
			depth = childDepth + 1
		} else if depth != childDepth+1 {
			t.Fatalf("unbalanced children in page %d", offset)
		}
	}
	return page.keys[0], depth
}
