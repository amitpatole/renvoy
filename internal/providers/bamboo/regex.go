package bamboo

// matchRegexOK reports whether a requirement match value is a regular
// expression Bamboo's java.util.regex accepts. It is deliberately stricter
// than Java: it accepts literals, ., ^, $, alternation, (...), (?:...),
// (?=...), (?!...), inline flags such as (?i) (but not x), simple character
// classes with ranges, and the *, +, ?, {n}, {n,}, {n,m} quantifiers with an
// optional lazy or possessive suffix, in at most 255 characters and 32 nested
// groups. Requirement values cannot contain backslashes, so no escapes are
// handled.
func matchRegexOK(s string) bool {
	rs := []rune(s)
	// Java compiles patterns recursively, so long or deeply nested ones
	// overflow its stack.
	if len(rs) > maxMatchLength {
		return false
	}
	depth := 0
	atom := false       // the previous token can take a quantifier
	quantified := false // the previous token is a quantifier without a suffix
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '(':
			if i+1 < len(rs) && rs[i+1] == '?' {
				j := i + 2
				for j < len(rs) && isFlag(rs[j]) {
					j++
				}
				switch {
				case j > i+2 && j < len(rs) && rs[j] == ')':
					// (?i) sets flags and is not an atom.
					i = j
					atom, quantified = false, false
					continue
				case j > i+2 && j < len(rs) && rs[j] == ':':
					i = j
				case j == i+2 && j < len(rs) && (rs[j] == ':' || rs[j] == '=' || rs[j] == '!'):
					i = j
				default:
					return false
				}
			}
			depth++
			if depth > maxMatchDepth {
				return false
			}
			atom, quantified = false, false
		case ')':
			if depth == 0 {
				return false
			}
			depth--
			atom, quantified = true, false
		case '|':
			atom, quantified = false, false
		case '^', '$':
			atom, quantified = false, false
		case '*', '+', '?':
			switch {
			case atom:
				atom, quantified = false, true
			case quantified && (c == '?' || c == '+'):
				// The lazy or possessive suffix of a quantifier.
				quantified = false
			default:
				return false
			}
		case '{':
			end, ok := repeat(rs, i)
			if !ok || !atom {
				return false
			}
			i = end
			atom, quantified = false, true
		case '[':
			end, ok := class(rs, i)
			if !ok {
				return false
			}
			i = end
			atom, quantified = true, false
		default:
			atom, quantified = true, false
		}
	}
	return depth == 0
}

// isFlag excludes x (comments mode), which changes how # and spaces parse.
// Bounds on requirement match values.
const (
	maxMatchLength = 255
	maxMatchDepth  = 32
)

func isFlag(r rune) bool { return r == 'i' || r == 'm' || r == 's' || r == 'u' || r == 'd' }

// repeat parses {n}, {n,} or {n,m} starting at rs[i] == '{' and returns the
// index of the closing brace.
func repeat(rs []rune, i int) (int, bool) {
	j := i + 1
	lo, n := digits(rs, j)
	if n == 0 {
		return 0, false
	}
	j += n
	hi := lo
	if j < len(rs) && rs[j] == ',' {
		j++
		v, m := digits(rs, j)
		j += m
		if m > 0 {
			hi = v
		} else {
			hi = lo
		}
	}
	if j >= len(rs) || rs[j] != '}' || hi < lo {
		return 0, false
	}
	return j, true
}

func digits(rs []rune, j int) (value, n int) {
	for j+n < len(rs) && rs[j+n] >= '0' && rs[j+n] <= '9' && n < 9 {
		value = value*10 + int(rs[j+n]-'0')
		n++
	}
	if j+n < len(rs) && rs[j+n] >= '0' && rs[j+n] <= '9' {
		return 0, 0
	}
	return value, n
}

// class parses a character class starting at rs[i] == '[' and returns the
// index of the closing bracket. Nested classes, intersections and a leading
// ']' are refused.
func class(rs []rune, i int) (int, bool) {
	j := i + 1
	if j < len(rs) && rs[j] == '^' {
		j++
	}
	start := j
	for j < len(rs) {
		c := rs[j]
		switch {
		case c == ']' && j > start:
			return j, true
		case c == '[' || c == ']' || c == '&':
			return 0, false
		case j+2 < len(rs) && rs[j+1] == '-' && rs[j+2] != ']':
			// A range a-z; Java refuses z-a and nesting brackets in a range.
			hi := rs[j+2]
			if hi < c || hi == '[' || hi == '&' || c == '-' {
				return 0, false
			}
			j += 3
			if j+1 < len(rs) && rs[j] == '-' && rs[j+1] != ']' {
				return 0, false
			}
			continue
		}
		j++
	}
	return 0, false
}
