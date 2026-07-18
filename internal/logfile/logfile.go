// Package logfile is a minimal size-rotating log writer, so `wlhl server` can
// keep durable, bounded logs on ANY platform (Windows included) without an OS
// supervisor (systemd/NSSM/launchd) or external rotation tooling. Deliberately
// dependency-free — rotation is rename-based: <path> → <path>.1 → <path>.2 …
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Writer struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	f        *os.File
	size     int64
}

// New opens (or creates, appending) the log file at path. maxMB is the size at
// which it rotates; backups is how many rotated files to keep.
func New(path string, maxMB, backups int) (*Writer, error) {
	if maxMB <= 0 {
		maxMB = 50
	}
	if backups < 0 {
		backups = 0
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("logfile dir: %w", err)
	}
	w := &Writer{path: path, maxBytes: int64(maxMB) << 20, backups: backups}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("logfile open: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("logfile stat: %w", err)
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			// Rotation failing must never lose log lines — keep writing to the
			// oversized file rather than dropping output.
			fmt.Fprintf(os.Stderr, "logfile: rotate failed: %v\n", err)
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate shifts path.N → path.N+1 (dropping the oldest), path → path.1, and
// reopens a fresh file. Callers hold w.mu.
func (w *Writer) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	w.f = nil
	// Any failure below leaves the ORIGINAL file in place (nothing renamed it
	// away yet on the error paths), so recover by reopening it — that keeps the
	// invariant "a failed rotate degrades to writing the oversized file, never
	// drops output". Without this, w.f stays nil and Write() wedges forever.
	recover := func(err error) error {
		if oerr := w.open(); oerr != nil {
			return fmt.Errorf("rotate failed (%v) and reopen failed: %w", err, oerr)
		}
		return err // rotate didn't happen, but the writer is live again
	}
	if w.backups == 0 {
		// no backups kept: truncate in place
		if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
			return recover(err)
		}
		return w.open()
	}
	for i := w.backups - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", w.path, i)
		to := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(from); err == nil {
			os.Remove(to)           // Windows: rename fails onto an existing file
			_ = os.Rename(from, to) // best-effort; a lost backup ≠ lost current log
		}
	}
	first := w.path + ".1"
	os.Remove(first)
	if err := os.Rename(w.path, first); err != nil && !os.IsNotExist(err) {
		return recover(err) // original still at w.path — reopen it, don't wedge
	}
	return w.open()
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
