package main

import (
	"fmt"
	"strings"
	"sync"
)

const (
	TypeBytes uint8 = iota + 1
	TypeInt64
)

type Value struct {
	Type uint8
	I64  int64
	Str  []byte
}

type Record struct {
	Cols []string
	Vals []Value
}

func (r *Record) AddStr(col string, val []byte) *Record {
	r.Cols = append(r.Cols, col)
	r.Vals = append(r.Vals, Value{Type: TypeBytes, Str: val})
	return r
}

func (r *Record) AddInt64(col string, val int64) *Record {
	r.Cols = append(r.Cols, col)
	r.Vals = append(r.Vals, Value{Type: TypeInt64, I64: val})
	return r
}

type TableDef struct {
	Name   string
	Types  []uint8
	Cols   []string
	PKeys  uint8 // The first PKeys columns form the primary key.
	Prefix uint8 // Assigned and persisted by CreateTable.
}

// DB provides single-row operations on tables stored in a KV file.
// Share one DB handle per file for concurrent access.
type DB struct {
	kv *KV
	mu sync.RWMutex
}

func NewDB(path string) *DB {
	return &DB{kv: NewKV(path)}
}

// CreateTable saves a definition and assigns its namespace in the key-value store.
func (db *DB) CreateTable(def TableDef) error {
	key, err := tableKey(def.Name)
	if err != nil {
		return err
	}
	if def.Prefix != 0 {
		return fmt.Errorf("table prefix is assigned by the database")
	}
	encoded, err := encodeTableDef(def)
	if err != nil {
		return err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if _, exists, err := db.kv.Get(key[:]); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("table %q already exists", def.Name)
	}

	// The all-zero schema key stores the last assigned table prefix.
	var sequence [maxKeySize]byte
	last, found, err := db.kv.Get(sequence[:])
	if err != nil {
		return err
	}
	prefix := byte(1)
	if found {
		if len(last) != 1 || last[0] == 0 {
			return fmt.Errorf("invalid table prefix counter")
		}
		if last[0] == 255 {
			return fmt.Errorf("table prefix space exhausted")
		}
		prefix = last[0] + 1
	}
	// Reserve first so a failed definition write can leave a gap but cannot reuse a prefix.
	if err := db.kv.Set(sequence[:], []byte{prefix}); err != nil {
		return err
	}
	encoded[0] = prefix
	return db.kv.Set(key[:], encoded)
}

func (db *DB) Table(name string) (TableDef, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.table(name)
}

func (db *DB) table(name string) (TableDef, error) {
	key, err := tableKey(name)
	if err != nil {
		return TableDef{}, err
	}
	encoded, found, err := db.kv.Get(key[:])
	if err != nil {
		return TableDef{}, err
	}
	if !found {
		return TableDef{}, fmt.Errorf("unknown table %q", name)
	}
	return decodeTableDef(name, encoded)
}

// Get looks up a primary key and fills record with the full row in schema order.
func (db *DB) Get(table string, record *Record) (bool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if record == nil {
		return false, fmt.Errorf("nil record")
	}
	def, err := db.table(table)
	if err != nil {
		return false, err
	}
	values, err := orderValues(def, *record, int(def.PKeys))
	if err != nil {
		return false, err
	}
	key, err := encodeRecordKey(def.Prefix, values)
	if err != nil {
		return false, err
	}
	data, found, err := db.kv.Get(key[:])
	if err != nil || !found {
		return false, err
	}
	fields, err := decodeRowValues(def.Types[def.PKeys:], data)
	if err != nil {
		return false, err
	}
	*record = Record{Cols: def.Cols, Vals: append(values, fields...)}
	return true, nil
}

// Insert writes a complete row if the primary key is absent.
func (db *DB) Insert(table string, record Record) (bool, error) {
	return db.writeRow(table, record, insertRow)
}

// Update replaces a complete row if the primary key is present.
func (db *DB) Update(table string, record Record) (bool, error) {
	return db.writeRow(table, record, updateRow)
}

// Upsert writes a complete row whether or not the primary key is present.
func (db *DB) Upsert(table string, record Record) error {
	_, err := db.writeRow(table, record, upsertRow)
	return err
}

type rowWrite uint8

const (
	insertRow rowWrite = iota
	updateRow
	upsertRow
)

func (db *DB) writeRow(table string, record Record, mode rowWrite) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	def, err := db.table(table)
	if err != nil {
		return false, err
	}
	values, err := orderValues(def, record, len(def.Cols))
	if err != nil {
		return false, err
	}
	key, err := encodeRecordKey(def.Prefix, values[:def.PKeys])
	if err != nil {
		return false, err
	}
	data, err := encodeRowValues(values[def.PKeys:])
	if err != nil {
		return false, err
	}
	if mode != upsertRow {
		_, found, err := db.kv.Get(key[:])
		if err != nil {
			return false, err
		}
		if (mode == insertRow && found) || (mode == updateRow && !found) {
			return false, nil
		}
	}
	if err := db.kv.Set(key[:], data); err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes a row by primary key.
func (db *DB) Delete(table string, record Record) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	def, err := db.table(table)
	if err != nil {
		return false, err
	}
	values, err := orderValues(def, record, int(def.PKeys))
	if err != nil {
		return false, err
	}
	key, err := encodeRecordKey(def.Prefix, values)
	if err != nil {
		return false, err
	}
	return db.kv.Del(key[:])
}

func tableKey(name string) ([maxKeySize]byte, error) {
	var key [maxKeySize]byte
	if len(name) == 0 || len(name) >= len(key) || strings.ContainsRune(name, 0) {
		return key, fmt.Errorf("table name must contain 1 to %d bytes without NUL", len(key)-1)
	}
	copy(key[1:], name)
	return key, nil
}

func encodeTableDef(def TableDef) ([]byte, error) {
	if len(def.Cols) != len(def.Types) || def.PKeys == 0 || int(def.PKeys) > len(def.Cols) {
		return nil, fmt.Errorf("table %q has invalid column types or primary key count", def.Name)
	}
	data := []byte{def.Prefix, def.PKeys}
	seen := make(map[string]bool)
	for i, col := range def.Cols {
		if col == "" || seen[col] || (def.Types[i] != TypeBytes && def.Types[i] != TypeInt64) {
			return nil, fmt.Errorf("table %q has invalid column %q", def.Name, col)
		}
		if len(data)+2+len(col) > maxValueSize {
			return nil, fmt.Errorf("table %q definition exceeds %d bytes", def.Name, maxValueSize)
		}
		seen[col] = true
		data = append(data, def.Types[i], byte(len(col)))
		data = append(data, col...)
	}
	return data, nil
}

func decodeTableDef(name string, data []byte) (TableDef, error) {
	if len(data) < 2 || data[0] == 0 {
		return TableDef{}, fmt.Errorf("invalid definition for table %q", name)
	}
	def := TableDef{Name: name, Prefix: data[0], PKeys: data[1]}
	for data = data[2:]; len(data) != 0; {
		if len(data) < 2 || int(data[1]) > len(data)-2 {
			return TableDef{}, fmt.Errorf("truncated definition for table %q", name)
		}
		end := 2 + int(data[1])
		def.Types = append(def.Types, data[0])
		def.Cols = append(def.Cols, string(data[2:end]))
		data = data[end:]
	}
	if _, err := encodeTableDef(def); err != nil {
		return TableDef{}, err
	}
	return def, nil
}
