package sqlrewrite

import "strings"

// RewriteCalls rewrites complete function calls from the inside out. The input
// must have its literals, comments, and delimited identifiers protected first.
// Commas inside nested parentheses never split the outer call's arguments.
func RewriteCalls(input string, rewrite func(string, []string) (string, bool)) string {
	var output strings.Builder
	for index := 0; index < len(input); {
		start := index
		if !isIdentifierByte(input[index]) {
			output.WriteByte(input[index])
			index++
			continue
		}
		for index < len(input) && isIdentifierByte(input[index]) {
			index++
		}
		name := input[start:index]
		open := index
		for open < len(input) && isSpace(input[open]) {
			open++
		}
		if open == len(input) || input[open] != '(' {
			output.WriteString(name)
			continue
		}
		close, _ := callArguments(input, open)
		if close < 0 {
			output.WriteString(input[start:])
			break
		}
		body := RewriteCalls(input[open+1:close], rewrite)
		// Rewrites can introduce parentheses and commas, so split the rewritten
		// body again before presenting it to the caller.
		_, args := callArguments("("+body+")", 0)
		if replacement, ok := rewrite(strings.ToUpper(name), args); ok {
			output.WriteString(replacement)
		} else {
			output.WriteString(input[start : open+1])
			output.WriteString(body)
			output.WriteByte(')')
		}
		index = close + 1
	}
	return output.String()
}

func callArguments(input string, open int) (int, []string) {
	depth, start := 1, open+1
	var args []string
	for index := start; index < len(input); index++ {
		switch input[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				if index > start || len(args) > 0 {
					args = append(args, strings.TrimSpace(input[start:index]))
				}
				return index, args
			}
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(input[start:index]))
				start = index + 1
			}
		}
	}
	return -1, nil
}

func isSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\f'
}
