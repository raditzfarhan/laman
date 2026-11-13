package laragon

import (
	"bufio"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var (
	collation0900 = regexp.MustCompile(`utf8mb4_0900_[a-z0-9_]+`)
	// A single versioned comment, non-greedy so it never spans past the first "*/".
	versionedComment = regexp.MustCompile(`/\*!(\d{5})[ \t].*?\*/`)
	definerClause    = regexp.MustCompile("DEFINER=(?:`[^`]*`|\x00[0-9]+\x00|\"[^\"]*\"|[A-Za-z0-9_$.-]+)@(?:`[^`]*`|\x00[0-9]+\x00|\"[^\"]*\"|[A-Za-z0-9_.%:-]+) ?")
)

var collationMap = map[string]string{
	"utf8mb4_0900_ai_ci": "utf8mb4_uca1400_ai_ci",
	"utf8mb4_0900_as_ci": "utf8mb4_uca1400_as_ci",
	"utf8mb4_0900_as_cs": "utf8mb4_uca1400_as_cs",
	"utf8mb4_0900_bin":   "utf8mb4_bin",
}

// Filter copies a mysqldump stream from src to dst, rewriting the MySQL 8
// statements MariaDB rejects. Data lines (INSERT ... / REPLACE ...) are
// copied byte-for-byte; every line keeps its original line ending.
func Filter(dst io.Writer, src io.Reader) error {
	r := bufio.NewReaderSize(src, 256<<10)
	inLit := false // inside a single-quoted literal continued from an earlier line
	w := bufio.NewWriterSize(dst, 256<<10)
	for {
		line, rerr := r.ReadString('\n')
		if line != "" {
			if _, err := w.WriteString(rewriteLine(line, &inLit)); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return w.Flush()
		}
		if rerr != nil {
			w.Flush()
			return rerr
		}
	}
}

var placeholder = regexp.MustCompile("\x00([0-9]+)\x00")

// scanLiteral returns the end index of a single-quoted literal body starting at
// i (already past the opening quote or at continued text) and whether it closed.
func scanLiteral(body string, i int) (int, bool) {
	for i < len(body) {
		switch body[i] {
		case '\x5c':
			i += 2
			continue
		case '\'':
			if i+1 < len(body) && body[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1, true
		}
		i++
	}
	if i > len(body) {
		i = len(body)
	}
	return i, false
}

// maskLiterals replaces every single-quoted literal in body with a placeholder
// token and returns the masked line plus the literals. *inLit carries an
// unterminated literal across lines. Backtick identifiers and "-- " comments
// are skipped so apostrophes inside them do not open a literal.
func maskLiterals(body string, inLit *bool) (string, []string) {
	var out strings.Builder
	var lits []string
	emit := func(lit string) {
		out.WriteString("\x00" + strconv.Itoa(len(lits)) + "\x00")
		lits = append(lits, lit)
	}
	i := 0
	for i < len(body) {
		if *inLit {
			j, closed := scanLiteral(body, i)
			emit(body[i:j])
			i = j
			*inLit = !closed
			continue
		}
		switch c := body[i]; {
		case c == '\'':
			j, closed := scanLiteral(body, i+1)
			emit(body[i:j])
			i = j
			*inLit = !closed
		case c == '`':
			j := strings.IndexByte(body[i+1:], '`')
			if j < 0 {
				out.WriteString(body[i:])
				i = len(body)
			} else {
				out.WriteString(body[i : i+j+2])
				i += j + 2
			}
		case c == '-' && strings.HasPrefix(body[i:], "-- "):
			out.WriteString(body[i:])
			i = len(body)
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), lits
}

func rewriteLine(line string, inLit *bool) string {
	if !*inLit && (strings.HasPrefix(line, "INSERT ") || strings.HasPrefix(line, "REPLACE ")) {
		return line
	}
	body, ending := line, ""
	if strings.HasSuffix(body, "\n") {
		body, ending = body[:len(body)-1], "\n"
		if strings.HasSuffix(body, "\r") {
			body, ending = body[:len(body)-1], "\r\n"
		}
	}
	if strings.IndexByte(body, 0) >= 0 { // cannot mask safely; still track state
		maskLiterals(body, inLit)
		return line
	}
	masked, lits := maskLiterals(body, inLit)
	if strings.Contains(masked, "utf8mb4_0900_") {
		masked = collation0900.ReplaceAllStringFunc(masked, func(m string) string {
			if to, ok := collationMap[m]; ok {
				return to
			}
			return "utf8mb4_uca1400_ai_ci"
		})
	}
	if strings.Contains(masked, "/*!") {
		masked = versionedComment.ReplaceAllStringFunc(masked, func(m string) string {
			if v, err := strconv.Atoi(m[3:8]); err == nil && v >= 80000 {
				return ""
			}
			return m
		})
	}
	if strings.Contains(masked, "DEFINER=") {
		masked = definerClause.ReplaceAllString(masked, "")
	}
	if len(lits) > 0 {
		masked = placeholder.ReplaceAllStringFunc(masked, func(m string) string {
			n, _ := strconv.Atoi(m[1 : len(m)-1])
			return lits[n]
		})
	}
	return masked + ending
}
