package bamboo_test

import (
	"testing"

	"github.com/amitpatole/renvoy/internal/providers/bamboo"
)

func TestQuartz(t *testing.T) {
	cases := map[string]string{
		"0 3 * * *":     "0 0 3 * * ?",
		"30 2 * * 1-5":  "0 30 2 ? * 2-6",
		"0 0 * * 0":     "0 0 0 ? * 1",
		"0 0 * * 6":     "0 0 0 ? * 7",
		"*/15 * * * *":  "0 */15 * * * ?",
		"0 9 * * */2":   "0 0 9 ? * */2",
		"0 9 * * 1/2":   "0 0 9 ? * 2/2",
		"0 9 * * 1,3,5": "0 0 9 ? * 2,4,6",
		"0 0 1 * *":     "0 0 0 1 * ?",
	}
	for in, want := range cases {
		got, err := bamboo.Quartz(in)
		if err != nil || got != want {
			t.Errorf("Quartz(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := bamboo.Quartz("0 0 1 * 1"); err == nil {
		t.Error("day of month and day of week together must be rejected")
	}
}
