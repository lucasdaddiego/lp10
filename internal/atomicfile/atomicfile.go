// Package atomicfile writes small files via a unique temporary sibling and an
// atomic rename, so a reader never observes a half-written file and concurrent
// writers cannot truncate or rename one another's temporary file. Shared by
// config (the premute/snapshot state) and artwork (the cover cache); both treat
// persistence as best-effort and ignore the error.
package atomicfile

import (
	"io"
	"os"
	"path/filepath"
)

// Write writes data to path via a unique temporary sibling and then renames it.
// Files are created 0600 (every caller persists private per-user state). The
// data is fsynced before the rename so a crash right after a save can't leave a
// journaled rename pointing at unwritten blocks (a zero-byte file behind the new
// name), and the directory is fsynced after it so the new name itself survives a
// crash (that sync's failure is ignored: the write is already durable-or-clean,
// and a filesystem that rejects directory fsync — some network mounts — should
// not fail an otherwise good save). On any failure the temporary file is removed
// and an existing target is left untouched.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
