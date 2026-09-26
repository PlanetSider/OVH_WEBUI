package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteJSONReclaimsFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteJSON(path, map[string]string{"ok": "yes"}); err != nil {
		t.Fatal(err)
	}
	fileLocksMu.Lock()
	remaining := len(fileLocks)
	fileLocksMu.Unlock()
	if remaining != 0 {
		t.Fatalf("file lock entries = %d, want 0 after operation", remaining)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
}
