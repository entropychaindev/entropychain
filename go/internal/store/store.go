// Package store persists blocks and the active chain in a bbolt database.
//
// Layout:
//
//	blocks/<hash>         -> block JSON (Python-compatible encoding)
//	main/<BE64 height>    -> block hash on the active chain
//
// Every write is a single bbolt transaction, so a crash leaves the database
// either before or after the change, never in between. This replaces the
// Python node's rewrite-everything chain.json.
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/entropychaindev/entropychain/go/internal/core"
)

var (
	bucketBlocks = []byte("blocks")
	bucketMain   = []byte("main")
)

// ErrNotFound is returned for unknown blocks.
var ErrNotFound = errors.New("not found")

// Store wraps the database.
type Store struct {
	db *bolt.DB
}

// Open opens (or creates) <dir>/chain.db.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(filepath.Join(dir, "chain.db"), 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, fmt.Errorf("chain database in %s is in use by another process", dir)
		}
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketBlocks, bucketMain} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func heightKey(h int64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, uint64(h))
	return k
}

// GetBlock loads a block by hash.
func (s *Store) GetBlock(hash string) (*core.Block, error) {
	var raw []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketBlocks).Get([]byte(hash))
		if v == nil {
			return ErrNotFound
		}
		raw = append([]byte(nil), v...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var b core.Block
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("corrupt block %s: %w", hash, err)
	}
	return &b, nil
}

// ForEachBlock calls fn for every stored block (in hash order).
func (s *Store) ForEachBlock(fn func(hash string, b *core.Block) error) error {
	return s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketBlocks).ForEach(func(k, v []byte) error {
			var b core.Block
			if err := json.Unmarshal(v, &b); err != nil {
				return fmt.Errorf("corrupt block %s: %w", k, err)
			}
			return fn(string(k), &b)
		})
	})
}

// MainChain returns the active chain's hashes by height.
func (s *Store) MainChain() ([]string, error) {
	var out []string
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMain).ForEach(func(k, v []byte) error {
			if int64(binary.BigEndian.Uint64(k)) != int64(len(out)) {
				return errors.New("main chain index has a gap")
			}
			out = append(out, string(v))
			return nil
		})
	})
	return out, err
}

// Batch is a set of writes applied atomically by Commit.
type Batch struct {
	blocks   map[string]*core.Block
	truncate int64 // remove main entries with height >= truncate (-1: none)
	setMain  map[int64]string
	delete   []string
}

// NewBatch starts an empty batch.
func NewBatch() *Batch {
	return &Batch{blocks: map[string]*core.Block{}, truncate: -1, setMain: map[int64]string{}}
}

// PutBlock stores a block body.
func (b *Batch) PutBlock(hash string, blk *core.Block) { b.blocks[hash] = blk }

// DeleteBlock removes a block body.
func (b *Batch) DeleteBlock(hash string) { b.delete = append(b.delete, hash) }

// TruncateMain drops active-chain entries at height >= h.
func (b *Batch) TruncateMain(h int64) { b.truncate = h }

// SetMain records hash at height on the active chain.
func (b *Batch) SetMain(height int64, hash string) { b.setMain[height] = hash }

// Commit applies the batch in one durable transaction.
func (s *Store) Commit(b *Batch) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		blocks, main := tx.Bucket(bucketBlocks), tx.Bucket(bucketMain)
		for h, blk := range b.blocks {
			raw, err := json.Marshal(blk)
			if err != nil {
				return err
			}
			if err := blocks.Put([]byte(h), raw); err != nil {
				return err
			}
		}
		for _, h := range b.delete {
			if err := blocks.Delete([]byte(h)); err != nil {
				return err
			}
		}
		if b.truncate >= 0 {
			// Collect first: deleting while iterating a bbolt cursor can skip keys.
			var doomed [][]byte
			c := main.Cursor()
			for k, _ := c.Seek(heightKey(b.truncate)); k != nil; k, _ = c.Next() {
				doomed = append(doomed, append([]byte(nil), k...))
			}
			for _, k := range doomed {
				if err := main.Delete(k); err != nil {
					return err
				}
			}
		}
		for height, h := range b.setMain {
			if err := main.Put(heightKey(height), []byte(h)); err != nil {
				return err
			}
		}
		return nil
	})
}
