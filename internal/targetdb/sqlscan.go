package targetdb

import (
	"regexp"
	"strings"
	"unicode"

	"dbmp/internal/model"
)

func Validate(m model.Migration) error {
	switch {
	case strings.TrimSpace(m.Name) == "" || len(m.Name) > 200:
		return model.Invalid("Nama migration wajib diisi (maksimal 200 karakter).")
	case m.Mode != model.ModeNormal && m.Mode != model.ModeBatch:
		return model.Invalid("Mode harus normal atau batch.")
	case m.IrreversibleReason == "" && strings.TrimSpace(m.DownSQL) == "":
		return model.Invalid("Isi SQL down, atau tandai migration sebagai irreversible beserta alasannya.")
	case m.IrreversibleReason != "" && strings.TrimSpace(m.DownSQL) != "":
		return model.Invalid("Migration irreversible tidak boleh memiliki SQL down.")
	}
	if err := validateSQL("up", m.UpSQL, m.Mode); err != nil {
		return err
	}
	if m.IrreversibleReason == "" {
		if err := validateSQL("down", m.DownSQL, m.Mode); err != nil {
			return err
		}
	}
	if m.Mode == model.ModeBatch {
		switch {
		case !identRe.MatchString(m.BatchTable) || !identRe.MatchString(m.BatchColumn):
			return model.Invalid("Batch: nama table (boleh schema.table) dan kolom cursor wajib diisi dan hanya huruf, angka, atau garis bawah.")
		case m.BatchSize < 1 || m.BatchSize > 100000:
			return model.Invalid("Batch size harus antara 1 dan 100000.")
		}
	}
	return nil
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

func validateSQL(which, sql, mode string) error {
	stmts := Split(sql)
	switch {
	case len(stmts) == 0:
		return model.Invalid("SQL " + which + " tidak boleh kosong.")
	case len(TransactionControl(stmts)) > 0:
		return model.Invalid("SQL " + which + ": statement " + TransactionControl(stmts)[0].Keyword +
			" tidak diizinkan; platform sudah menjalankan migration di dalam transaction.")
	case mode == model.ModeBatch && (len(stmts) != 1 || !strings.Contains(sql, "$1") || !strings.Contains(sql, "$2")):
		return model.Invalid("Batch: SQL " + which + " harus satu statement yang memakai $1 (cursor awal, eksklusif) dan $2 (cursor akhir, inklusif), " +
			"misalnya ... WHERE id > $1 AND id <= $2.")
	}
	return nil
}

type Statement struct {
	Text    string
	Keyword string
}

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
	i++
	for i < n {
		switch s[i] {
		case '\\':
			if backslashEscapes {
				i += 2
				continue
			}
		case q:
			if i+1 < n && s[i+1] == q {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return n
}

func dollarTag(s string, i int) (string, bool) {
	if i > 0 && isIdentChar(s[i-1]) {
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
			i++
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

func TransactionControl(stmts []Statement) []Statement {
	var bad []Statement
	for _, s := range stmts {
		if !transactionKeywords[s.Keyword] {
			continue
		}
		if s.Keyword == "PREPARE" && !strings.Contains(strings.ToUpper(s.Text), "TRANSACTION") {
			continue
		}
		bad = append(bad, s)
	}
	return bad
}

var explainable = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"WITH": true, "MERGE": true, "VALUES": true, "TABLE": true,
}

func (s Statement) Explainable() bool { return explainable[s.Keyword] }
