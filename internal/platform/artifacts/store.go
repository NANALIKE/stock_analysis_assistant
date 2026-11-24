package artifacts

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

type Metadata struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type Store struct{ Root string }

// Put compresses a raw response and atomically adds it to a content-addressed store.
// SHA256 and size refer to the stored gzip bytes, whose timestamp is deterministic.
func (s Store) Put(input io.Reader) (Metadata, error) {
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return Metadata{}, err
	}
	tmp, err := os.CreateTemp(s.Root, ".raw-*")
	if err != nil {
		return Metadata{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	hash := sha256.New()
	compressor := gzip.NewWriter(io.MultiWriter(tmp, hash))
	if _, err := io.Copy(compressor, input); err != nil {
		return Metadata{}, err
	}
	if err := compressor.Close(); err != nil {
		return Metadata{}, err
	}
	if err := tmp.Sync(); err != nil {
		return Metadata{}, err
	}
	info, err := tmp.Stat()
	if err != nil {
		return Metadata{}, err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	name := sum + ".jsonl.gz"
	// Link fails if the immutable target already exists; identical content is reused.
	if err := os.Link(tmp.Name(), filepath.Join(s.Root, name)); err != nil && !os.IsExist(err) {
		return Metadata{}, err
	}
	return Metadata{Path: name, SizeBytes: info.Size(), SHA256: sum}, nil
}
