package logfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"headless-launcher-go/internal/logfile"
)

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wlhl.log")
	// 1MB max, keep 2 backups
	w, err := logfile.New(p, 1, 2)
	if err != nil { t.Fatal(err) }
	line := strings.Repeat("x", 1023) + "\n" // 1KB lines
	for i := 0; i < 3500; i++ {              // ~3.4MB total → 2-3 rotations
		if _, err := w.Write([]byte(line)); err != nil { t.Fatal(err) }
	}
	w.Close()
	for _, f := range []string{p, p + ".1", p + ".2"} {
		st, err := os.Stat(f)
		if err != nil { t.Fatalf("%s missing: %v", f, err) }
		if st.Size() > 1<<20+2048 { t.Fatalf("%s oversize: %d", f, st.Size()) }
	}
	if _, err := os.Stat(p + ".3"); !os.IsNotExist(err) { t.Fatal("backup .3 should not exist (keep=2)") }
}
