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
	definerClause    = regexp.MustCompile("DEFINER=(?:`[^`]*`|'[^']*'|\"[^\"]*\"|[A-Za-z0-9_$.-]+)@(?:`[^`]*`|'[^']*'|\"[^\"]*\"|[A-Za-z0-9_.%:-]+) ?")
)

var collationMap = map[string]string{
	"utf8mb4_0900_ai_ci": "utf8mb4_uca1400_ai_ci",
	"utf8mb4_0900_as_ci": "utf8mb4_uca1400_as_ci",
	"utf8mb4_0900_as_cs": "utf8mb4_uca1400_as_cs",
	"utf8mb4_0900_bin":   "utf8mb4_bin",
}

// Filter copies a mysqldump stream from src to dst, rewriting the MySQL 8
// statements MariaDB rejects. Data lines (INSERT INTO / REPLACE INTO) are
// copied byte-for-byte; every line keeps its original line ending.
func Filter(dst io.Writer, src io.Reader) error {
	r := bufio.NewReaderSize(src, 256<<10)
	w := bufio.NewWriterSize(dst, 256<<10)
	for {
		line, rerr := r.ReadString('\n')
		if line != "" {
			if _, err := w.WriteString(rewriteLine(line)); err != nil {
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

func rewriteLine(line string) string {
	if strings.HasPrefix(line, "INSERT INTO ") || strings.HasPrefix(line, "REPLACE INTO ") {
		return line
	}
	body, ending := line, ""
	if strings.HasSuffix(body, "\n") {
		body, ending = body[:len(body)-1], "\n"
		if strings.HasSuffix(body, "\r") {
			body, ending = body[:len(body)-1], "\r\n"
		}
	}
	if strings.Contains(body, "utf8mb4_0900_") {
		body = collation0900.ReplaceAllStringFunc(body, func(m string) string {
			if to, ok := collationMap[m]; ok {
				return to
			}
			return "utf8mb4_uca1400_ai_ci"
		})
	}
	if strings.Contains(body, "/*!") {
		body = versionedComment.ReplaceAllStringFunc(body, func(m string) string {
			if v, err := strconv.Atoi(m[3:8]); err == nil && v >= 80000 {
				return ""
			}
			return m
		})
	}
	if strings.Contains(body, "DEFINER=") {
		body = definerClause.ReplaceAllString(body, "")
	}
	return body + ending
}
