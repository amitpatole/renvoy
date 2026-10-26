package bamboo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestJavaSpecsCompileAndValidate runs `mvn test` on every generated Java Specs
// golden project. The generated PlanSpecTest calls Bamboo's offline validation
// (EntityPropertiesBuilders.build), so this proves the output compiles against the
// real bamboo-specs library and that Bamboo accepts the plan. It needs a JDK,
// Maven, and network access, so it only runs when RENVOY_BAMBOO_JAVA_IT=1.
func TestJavaSpecsCompileAndValidate(t *testing.T) {
	if os.Getenv("RENVOY_BAMBOO_JAVA_IT") != "1" {
		t.Skip("set RENVOY_BAMBOO_JAVA_IT=1 to compile and validate Bamboo Java Specs with Maven")
	}
	mvn, err := exec.LookPath("mvn")
	if err != nil {
		t.Fatal("mvn is not on PATH")
	}
	projects, err := filepath.Glob("../../../testdata/golden/*/bamboo-java/bamboo-specs")
	if err != nil || len(projects) == 0 {
		t.Fatalf("no Java Specs goldens found: %v", err)
	}
	for _, project := range projects {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(project))), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(project)); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(mvn, "-B", "-q", "test")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("mvn test failed: %v\n%s", err, out)
			}
		})
	}
}
