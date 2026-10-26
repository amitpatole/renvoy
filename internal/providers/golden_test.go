package providers_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/amitpatole/renvoy/internal/config"
	"github.com/amitpatole/renvoy/internal/diagnostics"
	"github.com/amitpatole/renvoy/internal/providers"
	"github.com/amitpatole/renvoy/internal/providers/registry"
	"github.com/amitpatole/renvoy/internal/render"
)

var update = flag.Bool("update", false, "rewrite golden files")

const goldenDir = "../../testdata/golden"

// generate compiles a case for one provider and returns finalized files keyed by
// path plus the provider's diagnostics as text.
func generate(t *testing.T, src []byte, p providers.Provider) (map[string][]byte, string) {
	t.Helper()
	res := config.Compile(src, "renvoy.yaml", registry.Names())
	if res.Pipeline == nil {
		var b strings.Builder
		_ = diagnostics.WriteText(&b, res.Diagnostics)
		t.Fatalf("case does not compile:\n%s", b.String())
	}
	diags := append(res.Diagnostics, res.SourceMap.Locate(p.Validate(res.Pipeline))...).Sorted()
	var text strings.Builder
	_ = diagnostics.WriteText(&text, diags)
	if diags.HasErrors() {
		return nil, text.String()
	}
	files, err := p.Render(context.Background(), res.Pipeline, providers.RenderOptions{SourceName: "renvoy.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		b, err := render.Finalize(f, "renvoy.yaml", nil)
		if err != nil {
			t.Fatal(err)
		}
		out[f.Path] = b
	}
	return out, text.String()
}

func TestGolden(t *testing.T) {
	cases, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if !c.IsDir() {
			continue
		}
		dir := filepath.Join(goldenDir, c.Name())
		src, err := os.ReadFile(filepath.Join(dir, "renvoy.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range registry.All() {
			t.Run(c.Name()+"/"+p.Name(), func(t *testing.T) {
				files, diags := generate(t, src, p)
				again, diags2 := generate(t, src, p)
				if diags != diags2 || !sameFiles(files, again) {
					t.Fatal("generation is not deterministic")
				}
				checkGolden(t, filepath.Join(dir, p.Name()+".diagnostics.txt"), []byte(diags))
				outDir := filepath.Join(dir, p.Name())
				if *update {
					_ = os.RemoveAll(outDir)
				}
				for path, content := range files {
					checkGolden(t, filepath.Join(outDir, filepath.FromSlash(path)), content)
				}
				if !*update {
					assertNoExtraFiles(t, outDir, files)
				}
			})
		}
	}
}

func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file (run go test ./internal/providers -update): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file:\n%s", path,
			render.UnifiedDiff("golden", "got", want, got))
	}
}

func assertNoExtraFiles(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	var extra []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if _, ok := files[filepath.ToSlash(rel)]; !ok {
			extra = append(extra, rel)
		}
		return nil
	})
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("golden files no longer generated: %v (run with -update)", extra)
	}
}
