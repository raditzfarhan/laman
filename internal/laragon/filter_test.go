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
		"  `a` varchar(9) COLLATE utf8mb4_ja_0900_as_cs NOT NULL,":            "  `a` varchar(9) COLLATE utf8mb4_uca1400_as_cs NOT NULL,",
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
		t.Errorf("6-digit version altered: %q", got)
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

func TestFilterInsertVariants(t *testing.T) {
	for _, p := range []string{"INSERT IGNORE INTO", "INSERT DELAYED INTO", "REPLACE INTO", "INSERT INTO"} {
		line := p + " `notes` VALUES (1,'utf8mb4_0900_ai_ci DEFINER=`a`@`b` /*!80016 x */');\n"
		if got := runFilter(t, line); got != line {
			t.Errorf("%s altered: %q", p, got)
		}
	}
}

func TestFilterSkipsQuotedLiterals(t *testing.T) {
	cases := [][2]string{
		{"  `note` varchar(20) COLLATE utf8mb4_0900_ai_ci DEFAULT 'utf8mb4_0900_ai_ci' COMMENT 'see DEFINER=`a`@`b` /*!80016 x */',\n",
			"  `note` varchar(20) COLLATE utf8mb4_uca1400_ai_ci DEFAULT 'utf8mb4_0900_ai_ci' COMMENT 'see DEFINER=`a`@`b` /*!80016 x */',\n"},
		// backslash escape and doubled quote
		{"x 'it''s \x5c' utf8mb4_0900_bin' y utf8mb4_0900_bin\n", "x 'it''s \x5c' utf8mb4_0900_bin' y utf8mb4_bin\n"},
		// quotes inside versioned comments, kept and removed
		{"a /*!40100 DEFAULT 'x' */ /*!80016 DEFAULT ENCRYPTION='N' */;\n", "a /*!40100 DEFAULT 'x' */ ;\n"},
		// apostrophes in backticks and -- comments do not open a literal
		{"-- it's\nCREATE TABLE `it's` (a int) COLLATE=utf8mb4_0900_bin;\n", "-- it's\nCREATE TABLE `it's` (a int) COLLATE=utf8mb4_bin;\n"},
	}
	for _, c := range cases {
		if got := runFilter(t, c[0]); got != c[1] {
			t.Errorf("in %q\n got %q\nwant %q", c[0], got, c[1])
		}
	}
}

func TestFilterMultilineLiteral(t *testing.T) {
	in := "SET @a = 'line one utf8mb4_0900_ai_ci\nDEFINER=`a`@`b` /*!80016 x */ still text\n  INSERT INTO z' COLLATE utf8mb4_0900_bin;\nnext utf8mb4_0900_bin\n"
	want := "SET @a = 'line one utf8mb4_0900_ai_ci\nDEFINER=`a`@`b` /*!80016 x */ still text\n  INSERT INTO z' COLLATE utf8mb4_bin;\nnext utf8mb4_bin\n"
	if got := runFilter(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFilterPhantomLiteralDoesNotStick(t *testing.T) {
	ins := "INSERT INTO `n` VALUES ('utf8mb4_0900_bin');\n"
	for _, pre := range []string{
		"/* don't */\n",
		"# don't\n",
		"SELECT \"it's\";\n",
		"--\tdon't\n",
		"-- don't\n",
		"/* don't\n",
		"SELECT 'it' ; /* don't \n",
	} {
		if got := runFilter(t, pre+ins); got != pre+ins {
			t.Errorf("after %q: got %q", pre, got)
		}
	}
	// stuck state heals at DELIMITER / section comments / data lines
	if got := runFilter(t, "SELECT 'oops\nDELIMITER ;;\nx utf8mb4_0900_bin\n"); got != "SELECT 'oops\nDELIMITER ;;\nx utf8mb4_bin\n" {
		t.Errorf("DELIMITER heal: %q", got)
	}
	if got := runFilter(t, "SELECT 'oops\n-- Dumping\nx utf8mb4_0900_bin\n"); got != "SELECT 'oops\n-- Dumping\nx utf8mb4_bin\n" {
		t.Errorf("-- heal: %q", got)
	}
	if got := runFilter(t, "SELECT 'oops\n"+ins+"x utf8mb4_0900_bin\n"); got != "SELECT 'oops\n"+ins+"x utf8mb4_bin\n" {
		t.Errorf("INSERT heal: %q", got)
	}
}

func TestFilterVersionedCommentLiteralProtected(t *testing.T) {
	in := "/*!50003 SET x = 'utf8mb4_0900_bin' */ utf8mb4_0900_bin;\n"
	want := "/*!50003 SET x = 'utf8mb4_0900_bin' */ utf8mb4_bin;\n"
	if got := runFilter(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFilterDoubleQuotedStringProtected(t *testing.T) {
	in := "SELECT \"utf8mb4_0900_bin \"\"q\"\"\" , utf8mb4_0900_bin;\n"
	want := "SELECT \"utf8mb4_0900_bin \"\"q\"\"\" , utf8mb4_bin;\n"
	if got := runFilter(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFilterLanguageCollations(t *testing.T) {
	cases := map[string]string{
		"utf8mb4_ja_0900_as_cs":      "utf8mb4_uca1400_as_cs",
		"utf8mb4_ja_0900_as_cs_ks":   "utf8mb4_uca1400_as_cs",
		"utf8mb4_zh_0900_as_cs":      "utf8mb4_uca1400_as_cs",
		"utf8mb4_de_pb_0900_ai_ci":   "utf8mb4_uca1400_ai_ci",
		"utf8mb4_es_trad_0900_as_ci": "utf8mb4_uca1400_as_ci",
		"utf8mb4_sr_latn_0900_ai_ci": "utf8mb4_uca1400_ai_ci",
		"utf8mb4_ru_0900_as_cs":      "utf8mb4_uca1400_as_cs",
	}
	for in, want := range cases {
		line := "  `a` varchar(9) COLLATE " + in + " NOT NULL,\n"
		if got := runFilter(t, line); got != "  `a` varchar(9) COLLATE "+want+" NOT NULL,\n" {
			t.Errorf("%s: got %q", in, got)
		}
		ins := "INSERT INTO `t` VALUES ('" + in + "');\n"
		if got := runFilter(t, ins); got != ins {
			t.Errorf("INSERT altered: %q", got)
		}
	}
	// a quoted literal keeps the original name
	in := "  `a` varchar(9) COLLATE utf8mb4_ja_0900_as_cs DEFAULT 'utf8mb4_ja_0900_as_cs',\n"
	want := "  `a` varchar(9) COLLATE utf8mb4_uca1400_as_cs DEFAULT 'utf8mb4_ja_0900_as_cs',\n"
	if got := runFilter(t, in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
