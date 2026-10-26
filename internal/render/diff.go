package render

import (
	"fmt"
	"strings"
)

// maxDiffLines bounds the quadratic line diff after common prefix/suffix trimming
// (3000 lines needs a 36 MB table).
const maxDiffLines = 3000

const diffContext = 3

// UnifiedDiff returns a unified diff from a to b with three lines of context,
// or "" when they are equal.
func UnifiedDiff(aName, bName string, a, b []byte) string {
	if string(a) == string(b) {
		return ""
	}
	ops, ok := diffOps(splitLines(a), splitLines(b))
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", aName, bName)
	if !ok {
		out.WriteString("@@ files differ (too large to diff) @@\n")
		return out.String()
	}
	var changes []int
	for i, o := range ops {
		if o.kind != ' ' {
			changes = append(changes, i)
		}
	}
	for c := 0; c < len(changes); {
		first, last := changes[c], changes[c]
		c++
		for c < len(changes) && changes[c]-last <= 2*diffContext+1 {
			last = changes[c]
			c++
		}
		hunk := ops[max(first-diffContext, 0):min(last+diffContext+1, len(ops))]
		var aCount, bCount int
		for _, o := range hunk {
			if o.kind != '+' {
				aCount++
			}
			if o.kind != '-' {
				bCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(hunk[0].a, aCount), hunkRange(hunk[0].b, bCount))
		for _, o := range hunk {
			out.WriteByte(o.kind)
			out.WriteString(o.text)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func hunkRange(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	default:
		return fmt.Sprintf("%d,%d", start+1, count)
	}
}

type op struct {
	kind byte // ' ', '-', '+'
	text string
	a, b int // 0-based positions in a and b before this op
}

func diffOps(a, b []string) ([]op, bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ma) > maxDiffLines || len(mb) > maxDiffLines {
		return nil, false
	}
	var ops []op
	for i := 0; i < pre; i++ {
		ops = append(ops, op{' ', a[i], i, i})
	}
	n, m := len(ma), len(mb)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if ma[i] == mb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && ma[i] == mb[j]:
			ops = append(ops, op{' ', ma[i], pre + i, pre + j})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', ma[i], pre + i, pre + j})
			i++
		default:
			ops = append(ops, op{'+', mb[j], pre + i, pre + j})
			j++
		}
	}
	for k := 0; k < suf; k++ {
		ops = append(ops, op{' ', a[len(a)-suf+k], len(a) - suf + k, len(b) - suf + k})
	}
	return ops, true
}
