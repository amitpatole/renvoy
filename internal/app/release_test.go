package app

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// TestReleaseMetadataInSync pins the version and date in CITATION.cff and
// .zenodo.json to the latest released CHANGELOG entry, so a release cannot
// ship with stale citation metadata.
func TestReleaseMetadataInSync(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	m := regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\] - (\d{4}-\d{2}-\d{2})$`).FindStringSubmatch(read("../../CHANGELOG.md"))
	if m == nil {
		t.Fatal("CHANGELOG.md has no released version heading")
	}
	version, date := m[1], m[2]

	cff := read("../../CITATION.cff")
	if got := regexp.MustCompile(`(?m)^version: "([^"]*)"$`).FindStringSubmatch(cff); got == nil || got[1] != version {
		t.Errorf("CITATION.cff version = %v, want %q", got, version)
	}
	if got := regexp.MustCompile(`(?m)^date-released: "([^"]*)"$`).FindStringSubmatch(cff); got == nil || got[1] != date {
		t.Errorf("CITATION.cff date-released = %v, want %q", got, date)
	}

	if !regexp.MustCompile(`(?m)^doi: "10\.5281/zenodo\.\d+"$`).MatchString(cff) {
		t.Error("CITATION.cff has no Zenodo concept DOI")
	}

	var zenodo struct {
		Version            string `json:"version"`
		RelatedIdentifiers []struct {
			Identifier string `json:"identifier"`
		} `json:"related_identifiers"`
	}
	if err := json.Unmarshal([]byte(read("../../.zenodo.json")), &zenodo); err != nil {
		t.Fatal(err)
	}
	if zenodo.Version != version {
		t.Errorf(".zenodo.json version = %q, want %q", zenodo.Version, version)
	}
	pkg := "https://pkg.go.dev/github.com/amitpatole/renvoy@v" + version
	found := false
	for _, r := range zenodo.RelatedIdentifiers {
		found = found || r.Identifier == pkg
	}
	if !found {
		t.Errorf(".zenodo.json related_identifiers lacks %s", pkg)
	}
}
