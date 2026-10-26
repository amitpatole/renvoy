// Package config discovers and loads renvoy.yaml files.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/amitpatole/renvoy/internal/compiler"
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/pipeline"
)

// DefaultFile is the conventional config file name.
const DefaultFile = "renvoy.yaml"

// MaxFileSize bounds how much of a config file is read.
const MaxFileSize = 1 << 20

// candidates are tried in order when no file is given.
var candidates = []string{DefaultFile, "renvoy.yml"}

// Discover returns the config path to use. An explicit path wins; otherwise the
// first existing candidate in dir is returned, or DefaultFile when none exists.
func Discover(dir, explicit string) string {
	if explicit != "" {
		return explicit
	}
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, DefaultFile)
}

// ErrNotFound reports a missing config file.
var ErrNotFound = errors.New("config file not found")

// Read loads a config file, refusing non-regular files and files over MaxFileSize.
func Read(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxFileSize)
	}
	return data, nil
}

// Result is a loaded and compiled pipeline.
type Result struct {
	Pipeline    *pipeline.Pipeline
	SourceMap   *pipeline.SourceMap
	Diagnostics diagnostics.List
}

// Compile parses, validates, and normalizes config bytes. The pipeline is nil
// when any error diagnostic was produced. displayName labels diagnostics.
func Compile(data []byte, displayName string, knownExtensions []string) Result {
	p, sm, diags := pipeline.Parse(data, displayName)
	if p == nil {
		return Result{SourceMap: sm, Diagnostics: diags}
	}
	diags = append(diags, sm.Locate(pipeline.Validate(p, knownExtensions))...)
	if diags.HasErrors() {
		return Result{SourceMap: sm, Diagnostics: diags}
	}
	diags = append(diags, sm.Locate(compiler.Normalize(p))...)
	if diags.HasErrors() {
		return Result{SourceMap: sm, Diagnostics: diags}
	}
	return Result{Pipeline: p, SourceMap: sm, Diagnostics: diags}
}
