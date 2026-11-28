package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestKVSetGetAndDeleteAcrossPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	db := NewKV(path)
	requireKV(t, db, []byte("missing"), nil, false)
	if deleted, err := db.Del([]byte("missing")); err != nil || deleted {
		t.Fatalf("delete from missing file = %t, %v", deleted, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reading an absent key created a file: %v", err)
	}

	const keyCount = 120
	for i := range keyCount {
		key := byte((i * 37) % keyCount)
		if err := db.Set([]byte{key, 0}, []byte{0, key}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []byte{0, 60, 119} {
		if err := db.Set([]byte{key, 0}, []byte("updated")); err != nil {
			t.Fatal(err)
		}
	}
	db = NewKV(path)
	for key := range byte(keyCount) {
		want := []byte{0, key}
		if key == 0 || key == 60 || key == 119 {
			want = []byte("updated")
		}
		requireKV(t, db, []byte{key, 0}, want, true)
	}
	for i := range keyCount {
		key := []byte{byte((i * 23) % keyCount), 0}
		if deleted, err := db.Del(key); err != nil || !deleted {
			t.Fatalf("delete %x = %t, %v", key, deleted, err)
		}
		requireKV(t, db, key, nil, false)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) < pageHeaderSize || binary.BigEndian.Uint64(data[1:pageHeaderSize]) != 0 {
		t.Fatalf("deleting every key did not clear the root: %v", err)
	}
	if deleted, err := db.Del([]byte{0, 0}); err != nil || deleted {
		t.Fatalf("delete from empty tree = %t, %v", deleted, err)
	}
	if err := db.Set(nil, nil); err != nil {
		t.Fatal(err)
	}
	requireKV(t, NewKV(path), nil, nil, true)
}

func TestKVPersistsDistinctByteKeysAndValidatesSizes(t *testing.T) {
	db := NewKV(filepath.Join(t.TempDir(), "kv.db"))
	for i, key := range [][]byte{nil, {0}, {0, 0}, {1}, {0, 1}, {0xff}} {
		if err := db.Set(key, []byte{0, byte(i), 0}); err != nil {
			t.Fatal(err)
		}
	}
	for i, key := range [][]byte{nil, {0}, {0, 0}, {1}, {0, 1}, {0xff}} {
		requireKV(t, db, key, []byte{0, byte(i), 0}, true)
	}
	tooLong := make([]byte, maxKeySize+1)
	if err := db.Set(tooLong, nil); err == nil {
		t.Fatal("Set accepted an oversized key")
	}
	if err := db.Set([]byte{1}, make([]byte, maxValueSize+1)); err == nil {
		t.Fatal("Set accepted an oversized value")
	}
	if _, _, err := db.Get(tooLong); err == nil {
		t.Fatal("Get accepted an oversized key")
	}
	if _, err := db.Del(tooLong); err == nil {
		t.Fatal("Del accepted an oversized key")
	}
	requireKV(t, db, []byte{1}, []byte{0, 3, 0}, true)
	maxValue := bytes.Repeat([]byte{0, 255}, maxValueSize/2)
	if err := db.Set([]byte{1}, maxValue); err != nil {
		t.Fatal(err)
	}
	requireKV(t, db, []byte{1}, maxValue, true)
}

func TestKVOperatesOnLegacyIntegerRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	tree := NewBPTreeDisk(path)
	count := int64(leafMaxKV + 5)
	for i := range count {
		if err := tree.Insert(-1, i); err != nil {
			t.Fatal(err)
		}
	}
	key := binary.BigEndian.AppendUint64(nil, ^uint64(0))
	db := NewKV(path)
	requireKV(t, db, key, binary.BigEndian.AppendUint64(nil, uint64(count-1)), true)
	if err := db.Set(key, []byte("new")); err != nil {
		t.Fatal(err)
	}
	requireKV(t, db, key, []byte("new"), true)
	for i := count - 1; i >= 0; i-- {
		if deleted, err := db.Del(key); err != nil || !deleted {
			t.Fatalf("delete duplicate %d = %t, %v", i, deleted, err)
		}
		if i > 0 {
			requireKV(t, db, key, binary.BigEndian.AppendUint64(nil, uint64(i-1)), true)
		}
	}
	requireKV(t, db, key, nil, false)
}

func requireKV(t *testing.T, db *KV, key, want []byte, wantFound bool) {
	t.Helper()
	got, found, err := db.Get(key)
	if err != nil || found != wantFound || !bytes.Equal(got, want) {
		t.Fatalf("Get(%x) = %x, %t, %v, want %x, %t, nil", key, got, found, err, want, wantFound)
	}
}
