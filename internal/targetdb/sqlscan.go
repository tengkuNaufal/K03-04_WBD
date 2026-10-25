package targetdb

import (
	"strings"
	"unicode"

	"dbmp/internal/model"
)

// Validate checks a migration before it is saved or executed.
func Validate(name, sql string) error {
	stmts := Split(sql)
	switch {
	case strings.TrimSpace(name) == "" || len(name) > 200:
		return model.Invalid("Nama migration wajib diisi (maksimal 200 karakter).")
	case len(stmts) == 0:
		return model.Invalid("SQL migration tidak boleh kosong.")
	case len(TransactionControl(stmts)) > 0:
		return model.Invalid("Statement " + TransactionControl(stmts)[0].Keyword +
			" tidak diizinkan: platform sudah menjalankan migration di dalam transaction.")
	}
	return nil
}

// Statement is one SQL statement of a migration script.
type Statement struct {
	Text    string // statement without the trailing semicolon
	Keyword string // first keyword, upper case (SELECT, CREATE, ...)
}

// Split cuts a script into statements. It understands the PostgreSQL lexical
// elements that may contain a semicolon: 'strings' (including E” escapes),
// "identifiers", $tag$ dollar quoted bodies $tag$, -- line comments and
// nested /* block comments */.
func Split(script string) []Statement {
	var out []Statement
	start := 0
	i := 0
	n := len(script)
	flush := func(end int) {
		text := strings.TrimSpace(script[start:end])
		if kw := firstKeyword(text); kw != "" {
			out = append(out, Statement{Text: text, Keyword: kw})
		}
	}
	for i < n {
		c := script[i]
		switch {
		case c == '-' && i+1 < n && script[i+1] == '-':
			for i < n && script[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && script[i+1] == '*':
			i = skipBlockComment(script, i)
		case c == '\'':
			escapes := i > 0 && (script[i-1] == 'E' || script[i-1] == 'e') && (i < 2 || !isIdentChar(script[i-2]))
			i = skipQuoted(script, i, '\'', escapes)
		case c == '"':
			i = skipQuoted(script, i, '"', false)
		case c == '$':
			if tag, ok := dollarTag(script, i); ok {
				end := strings.Index(script[i+len(tag):], tag)
				if end < 0 {
					i = n
				} else {
					i = i + len(tag) + end + len(tag)
				}
			} else {
				i++
			}
		case c == ';':
			flush(i)
			i++
			start = i
		default:
			i++
		}
	}
	flush(n)
	return out
}

func isIdentChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func skipBlockComment(s string, i int) int {
	depth := 0
	n := len(s)
	for i < n {
		if s[i] == '/' && i+1 < n && s[i+1] == '*' {
			depth++
			i += 2
			continue
		}
		if s[i] == '*' && i+1 < n && s[i+1] == '/' {
			depth--
			i += 2
			if depth == 0 {
				return i
			}
			continue
		}
		i++
	}
	return n
}

func skipQuoted(s string, i int, q byte, backslashEscapes bool) int {
	n := len(s)
	i++ // opening quote
	for i < n {
		switch s[i] {
		case '\\':
			if backslashEscapes {
				i += 2
				continue
			}
		case q:
			if i+1 < n && s[i+1] == q { // doubled quote
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return n
}

// dollarTag returns "$tag$" when position i starts a dollar quote.
func dollarTag(s string, i int) (string, bool) {
	if i > 0 && isIdentChar(s[i-1]) { // e.g. a parameter like foo$1 is not a quote
		return "", false
	}
	j := i + 1
	for j < len(s) && s[j] != '$' {
		c := s[j]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 || (j > i+1 && c >= '0' && c <= '9')) {
			return "", false
		}
		j++
	}
	if j >= len(s) {
		return "", false
	}
	return s[i : j+1], true
}

// firstKeyword returns the first word of a statement, skipping comments.
func firstKeyword(s string) string {
	i := 0
	n := len(s)
	for i < n {
		switch {
		case unicode.IsSpace(rune(s[i])):
			i++
		case s[i] == '-' && i+1 < n && s[i+1] == '-':
			for i < n && s[i] != '\n' {
				i++
			}
		case s[i] == '/' && i+1 < n && s[i+1] == '*':
			i = skipBlockComment(s, i)
		case s[i] == '(':
			i++ // (SELECT ...) UNION ...
		default:
			j := i
			for j < n && isIdentChar(s[j]) {
				j++
			}
			return strings.ToUpper(s[i:j])
		}
	}
	return ""
}

var transactionKeywords = map[string]bool{
	"BEGIN": true, "START": true, "COMMIT": true, "END": true,
	"ROLLBACK": true, "ABORT": true, "PREPARE": true,
}

// TransactionControl returns the statements that would interfere with the
// transaction the platform wraps around every migration.
func TransactionControl(stmts []Statement) []Statement {
	var bad []Statement
	for _, s := range stmts {
		if !transactionKeywords[s.Keyword] {
			continue
		}
		if s.Keyword == "PREPARE" && !strings.Contains(strings.ToUpper(s.Text), "TRANSACTION") {
			continue // PREPARE of a prepared statement is fine
		}
		bad = append(bad, s)
	}
	return bad
}

var explainable = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"WITH": true, "MERGE": true, "VALUES": true, "TABLE": true,
}

// Explainable reports whether PostgreSQL accepts EXPLAIN for the statement.
func (s Statement) Explainable() bool { return explainable[s.Keyword] }
