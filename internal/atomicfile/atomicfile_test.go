package atomicfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func assertNoTemps(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatalf("glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("temporary files should be gone, found %v", matches)
	}
}

func TestWriteSuccessRemovesTemp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")
	if err := Write(path, []byte("test content")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "test content" {
		t.Errorf("content = %q, err %v; want the written data", b, err)
	}
	assertNoTemps(t, path)
}

func TestWriteFailedCreateLeavesNoTemp(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "nonexistent", "test.txt")
	if err := Write(badPath, []byte("test")); err == nil {
		t.Error("Write into a missing parent should report an error")
	}
}

// Renaming the temporary file onto an existing directory fails, the sibling is
// cleaned up, and the target is left untouched.
func TestWriteRenameOntoDirFailsAndCleansUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, []byte("data")); err == nil {
		t.Error("rename onto a directory should report an error")
	}
	assertNoTemps(t, dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Error("target should still be a directory after the failed write")
	}
}

func TestWriteConcurrentWritersLeaveOneWholePayload(t *testing.T) {
	const writers = 32

	path := filepath.Join(t.TempDir(), "snapshot.json")
	payloads := make([][]byte, writers)
	expected := make(map[string]struct{}, writers)
	for i := range payloads {
		prefix := fmt.Appendf(nil, "writer-%02d:", i)
		payloads[i] = append(prefix, bytes.Repeat([]byte{byte('a' + i%26)}, 128<<10)...)
		expected[string(payloads[i])] = struct{}{}
	}

	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for _, payload := range payloads {
		wg.Go(func() {
			<-start
			errs <- Write(path, payload)
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Write: %v", err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if _, ok := expected[string(got)]; !ok {
		t.Errorf("final file is not one complete writer payload (size %d)", len(got))
	}
	assertNoTemps(t, path)
}
