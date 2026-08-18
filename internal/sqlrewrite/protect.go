package sqlrewrite

import (
	"fmt"
	"strings"
)

// Protected keeps string literals and comments out of regular-expression based
// SQL rewrites. SQL contains opaque placeholders that can be restored after the
// rewrite. National string prefixes (N'...') can be removed while protecting
// the literal because PostgreSQL string literals are Unicode by default.
type Protected struct {
	SQL       string
	fragments []string
	prefix    string
}

func Protect(input string, normalizeNationalStrings bool) Protected {
	prefix := "\x00ms2pg_protected_"
	for strings.Contains(input, prefix) {
		prefix += "_"
	}

	protected := Protected{prefix: prefix}
	var output strings.Builder
	output.Grow(len(input))

	for index := 0; index < len(input); {
		start := index
		nationalString := false
		if normalizeNationalStrings && (input[index] == 'N' || input[index] == 'n') &&
			index+1 < len(input) && input[index+1] == '\'' &&
			(index == 0 || !isIdentifierByte(input[index-1])) {
			nationalString = true
			index++
		}

		if input[index] == '\'' {
			literalStart := index
			index++
			for index < len(input) {
				if input[index] != '\'' {
					index++
					continue
				}
				index++
				if index < len(input) && input[index] == '\'' {
					index++
					continue
				}
				break
			}
			fragmentStart := start
			if nationalString {
				fragmentStart = literalStart
			}
			protected.add(&output, input[fragmentStart:index])
			continue
		}

		if index+1 < len(input) && input[index] == '-' && input[index+1] == '-' {
			index += 2
			for index < len(input) && input[index] != '\n' {
				index++
			}
			protected.add(&output, input[start:index])
			continue
		}

		if index+1 < len(input) && input[index] == '/' && input[index+1] == '*' {
			index += 2
			depth := 1
			for index < len(input) && depth > 0 {
				if index+1 < len(input) && input[index] == '/' && input[index+1] == '*' {
					depth++
					index += 2
					continue
				}
				if index+1 < len(input) && input[index] == '*' && input[index+1] == '/' {
					depth--
					index += 2
					continue
				}
				index++
			}
			protected.add(&output, input[start:index])
			continue
		}

		if nationalString {
			// The N was not followed by a string after all. This is defensive;
			// the condition above normally makes this branch unreachable.
			output.WriteByte(input[start])
			index = start + 1
			continue
		}

		output.WriteByte(input[index])
		index++
	}

	protected.SQL = output.String()
	return protected
}

func (protected Protected) Restore(input string) string {
	for index, fragment := range protected.fragments {
		input = strings.ReplaceAll(input, protected.placeholder(index), fragment)
	}
	return input
}

func (protected *Protected) add(output *strings.Builder, fragment string) {
	output.WriteString(protected.placeholder(len(protected.fragments)))
	protected.fragments = append(protected.fragments, fragment)
}

func (protected Protected) placeholder(index int) string {
	return fmt.Sprintf("%s%d\x00", protected.prefix, index)
}

func isIdentifierByte(value byte) bool {
	return value == '_' || value == '$' || value == '#' || value == '@' ||
		value >= '0' && value <= '9' ||
		value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z'
}
