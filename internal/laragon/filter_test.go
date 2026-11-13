package laragon

import (
	"bytes"
	"strings"
	"testing"
)

func runFilter(t *testing.T, in string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Filter(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("Filter: %v", err)
	}
	return out.String()
}

func TestFilterCollations(t *testing.T) {
	cases := map[string]string{
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;": ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci;",
		"  `a` varchar(9) COLLATE utf8mb4_0900_as_ci NOT NULL,":               "  `a` varchar(9) COLLATE utf8mb4_uca1400_as_ci NOT NULL,",
		"  `a` varchar(9) COLLATE utf8mb4_0900_as_cs NOT NULL,":               "  `a` varchar(9) COLLATE utf8mb4_uca1400_as_cs NOT NULL,",
		"  `a` varchar(9) COLLATE utf8mb4_0900_bin NOT NULL,":                 "  `a` varchar(9) COLLATE utf8mb4_bin NOT NULL,",
		"  `a` varchar(9) COLLATE utf8mb4_0900_ja_0900_as_cs NOT NULL,":       "  `a` varchar(9) COLLATE utf8mb4_uca1400_ai_ci NOT NULL,",
		"  `a` varchar(9) COLLATE utf8mb4_unicode_ci NOT NULL,":               "  `a` varchar(9) COLLATE utf8mb4_unicode_ci NOT NULL,",
	}
	for in, want := range cases {
		if got := runFilter(t, in+"\n"); got != want+"\n" {
			t.Errorf("in %q\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestFilterVersionedComments(t *testing.T) {
	in := "CREATE DATABASE `acme` /*!40100 DEFAULT CHARACTER SET utf8mb4 */ /*!80016 DEFAULT ENCRYPTION='N' */;\n"
	want := "CREATE DATABASE `acme` /*!40100 DEFAULT CHARACTER SET utf8mb4 */ ;\n"
	if got := runFilter(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// non-greedy: a kept comment after a removed one survives
	in = "/*!80023 a */ x /*!50001 keep */ y\n"
	want = " x /*!50001 keep */ y\n"
	if got := runFilter(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// 6-digit versions
	if got := runFilter(t, "a /*!100000 z */ b\n"); got != "a /*!100000 z */ b\n" {
		t.Errorf("6-digit version 100000 is MariaDB-style, wait: got %q", got)
	}
}

func TestFilterDefiner(t *testing.T) {
	cases := map[string]string{
		"/*!50013 DEFINER=`root`@`localhost` SQL SECURITY DEFINER */":                                                                 "/*!50013 SQL SECURITY DEFINER */",
		"CREATE DEFINER=`root`@`%` PROCEDURE `p`()":                                                                                   "CREATE PROCEDURE `p`()",
		"/*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `n`":                                             "/*!50017 */ /*!50003 TRIGGER `t` BEFORE INSERT ON `n`",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50013 DEFINER=`a b`@`h` SQL SECURITY DEFINER */ /*!50001 VIEW `v` AS select 1 */;": "/*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50013 SQL SECURITY DEFINER */ /*!50001 VIEW `v` AS select 1 */;",
		"CREATE DEFINER='root'@'localhost' FUNCTION `f`()":                                                                            "CREATE FUNCTION `f`()",
	}
	for in, want := range cases {
		if got := runFilter(t, in+"\n"); got != want+"\n" {
			t.Errorf("in %q\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestFilterLeavesDataAlone(t *testing.T) {
	line := "INSERT INTO `notes` VALUES (1,'utf8mb4_0900_ai_ci DEFINER=`a`@`b` /*!80016 x */');"
	for _, tail := range []string{"\n", "\r\n", ""} {
		if got := runFilter(t, line+tail); got != line+tail {
			t.Errorf("tail %q: got %q", tail, got)
		}
	}
}

func TestFilterPreservesLineEndings(t *testing.T) {
	in := "a utf8mb4_0900_bin\r\nb\nINSERT INTO `t` VALUES ('\r');\r\nlast utf8mb4_0900_bin"
	want := "a utf8mb4_bin\r\nb\nINSERT INTO `t` VALUES ('\r');\r\nlast utf8mb4_bin"
	if got := runFilter(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFilterMultilineRoutineBody(t *testing.T) {
	in := "DELIMITER ;;\n/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `n` FOR EACH ROW BEGIN\n  SET NEW.a = 1;\nEND */;;\nDELIMITER ;\n"
	want := "DELIMITER ;;\n/*!50003 CREATE*/ /*!50017 */ /*!50003 TRIGGER `t` BEFORE INSERT ON `n` FOR EACH ROW BEGIN\n  SET NEW.a = 1;\nEND */;;\nDELIMITER ;\n"
	if got := runFilter(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFilterLongLine(t *testing.T) {
	line := "INSERT INTO `big` VALUES ('" + strings.Repeat("x", 20<<20) + "');\n"
	var out bytes.Buffer
	if err := Filter(&out, strings.NewReader(line)); err != nil {
		t.Fatal(err)
	}
	if out.String() != line {
		t.Fatalf("long line altered (len %d vs %d)", out.Len(), len(line))
	}
}
