//go:build unix

package config_test

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/amitpatole/renvoy/internal/config"
)

// Opening a FIFO for reading blocks until a writer appears; Read must refuse it
// without opening it.
func TestReadRefusesFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "renvoy.yaml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := config.Read(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was accepted as a config file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read blocked on a FIFO")
	}
}
