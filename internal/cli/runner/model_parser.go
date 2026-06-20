package runner

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/tradalab/scorix/internal/cli/runner/dialect"
)

func toCamelCase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, "")
}

func lowerFirstRune(s string) string {
	if s == "" {
		return s
	}
	r := s[0]
	if r >= 'A' && r <= 'Z' {
		return string(r+32) + s[1:]
	}
	return s
}

// unquoteIdent strips one layer of "x" / `x` / [x] wrapping (authors quote
// reserved words like group/order/user); stored unquoted for clean Go names.
func unquoteIdent(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	switch {
	case first == '"' && last == '"':
		return s[1 : len(s)-1]
	case first == '`' && last == '`':
		return s[1 : len(s)-1]
	case first == '[' && last == ']':
		return s[1 : len(s)-1]
	}
	return s
}

// goFieldFromColumn applies the *_id → *ID convention.
func goFieldFromColumn(colName string) string {
	if strings.ToLower(colName) == "id" {
		return "ID"
	}
	if strings.HasSuffix(strings.ToLower(colName), "_id") {
		return toCamelCase(colName[:len(colName)-3]) + "ID"
	}
	return toCamelCase(colName)
}

// Identifier captures accept "x" / `x` / [x] / x — unquoteIdent strips wrappers post-match.
var (
	tableHeadRegex = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` +
		"(\"[a-zA-Z0-9_]+\"|`[a-zA-Z0-9_]+`|\\[[a-zA-Z0-9_]+\\]|[a-zA-Z0-9_]+)" +
		`\s*`)
	columnRegex = regexp.MustCompile(`(?i)^` +
		"(\"[^\"]+\"|`[^`]+`|\\[[^\\]]+\\]|[a-zA-Z0-9_]+)" +
		`\s+([a-zA-Z0-9_]+)(?:\([0-9,]+\))?(.*)$`)

	tablePKRegex     = regexp.MustCompile(`(?i)PRIMARY\s+KEY\s*\(\s*([a-zA-Z0-9_,\s]+?)\s*\)`)
	tableUniqueRegex = regexp.MustCompile(`(?i)^UNIQUE\s*\(\s*([a-zA-Z0-9_,\s]+?)\s*\)`)
)

// isWordBoundary reports whether b is not part of a SQL identifier token.
func isWordBoundary(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '_':
		return false
	default:
		return true
	}
}

// indexKeyword finds the first whole-word occurrence of keyword (offset, or -1).
// Whole-word so a column like `is_default` doesn't match DEFAULT.
func indexKeyword(upper, keyword string) int {
	from := 0
	for {
		rel := strings.Index(upper[from:], keyword)
		if rel < 0 {
			return -1
		}
		idx := from + rel
		beforeOK := idx == 0 || isWordBoundary(upper[idx-1])
		afterIdx := idx + len(keyword)
		afterOK := afterIdx == len(upper) || isWordBoundary(upper[afterIdx])
		if beforeOK && afterOK {
			return idx
		}
		from = idx + 1
	}
}

// extractDefaultValue returns the raw token after DEFAULT — a single-quoted literal,
// a paren-balanced expression, or a bareword. Whole-word match on string-blanked text
// so substrings like `is_default` don't trigger.
func extractDefaultValue(line string) string {
	blanked := strings.ToUpper(blankStringLiterals(line))
	idx := indexKeyword(blanked, "DEFAULT")
	if idx < 0 {
		return ""
	}
	rest := line[idx+len("DEFAULT"):]
	if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '\t') {
		return ""
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return ""
	}

	switch rest[0] {
	case '\'':
		i := 1
		for i < len(rest) {
			if rest[i] == '\'' {
				if i+1 < len(rest) && rest[i+1] == '\'' {
					i += 2
					continue
				}
				return rest[:i+1]
			}
			i++
		}
		return rest
	case '(':
		depth := 0
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case '\'':
				j := i + 1
				for j < len(rest) {
					if rest[j] == '\'' {
						if j+1 < len(rest) && rest[j+1] == '\'' {
							j += 2
							continue
						}
						break
					}
					j++
				}
				i = j
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return rest[:i+1]
				}
			}
		}
		return rest
	default:
		i := 0
		for i < len(rest) {
			c := rest[i]
			if c == ' ' || c == '\t' || c == ',' || c == ')' {
				break
			}
			i++
		}
		return rest[:i]
	}
}

func splitColList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseSQLSchema produces []sqlTable from a CREATE TABLE script. FOREIGN KEY
// clauses are ignored — relations live in internal/logic/.
func parseSQLSchema(schemaPath string, d dialect.Dialect) ([]sqlTable, error) {
	b, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}

	// Match on sanitized text (CREATE TABLE / parens inside string literals ignored);
	// read body and name from raw so DEFAULT values stay verbatim.
	raw := string(b)
	sanitized := blankStringLiterals(raw)
	heads := tableHeadRegex.FindAllStringSubmatchIndex(sanitized, -1)

	seen := make(map[string]bool)
	var tables []sqlTable
	for _, m := range heads {
		if len(m) < 4 {
			continue
		}
		open, close, ok := tableBodySpan(sanitized, m[1])
		if !ok {
			continue
		}
		name := unquoteIdent(raw[m[2]:m[3]])
		if seen[name] {
			continue
		}
		seen[name] = true
		tables = append(tables, parseTable(name, raw[open+1:close], d))
	}
	return tables, nil
}

// tableBodySpan returns the body's opening/closing paren offsets, scanning depth over
// string-blanked text (parens in literals don't shift depth). ok is false on unbalanced
// parens — skip rather than mis-parse truncated DDL.
func tableBodySpan(blanked string, from int) (open, close int, ok bool) {
	open = strings.IndexByte(blanked[from:], '(')
	if open < 0 {
		return 0, 0, false
	}
	open += from
	depth := 0
	for i := open; i < len(blanked); i++ {
		switch blanked[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return open, i, true
			}
		}
	}
	return 0, 0, false
}

// blankStringLiterals fills single-quoted contents with spaces (length-preserving) so regexes skip embedded SQL.
func blankStringLiterals(s string) string {
	buf := []byte(s)
	for i := 0; i < len(buf); i++ {
		if buf[i] != '\'' {
			continue
		}
		j := i + 1
		for j < len(buf) {
			if buf[j] == '\'' {
				if j+1 < len(buf) && buf[j+1] == '\'' {
					buf[j] = ' '
					buf[j+1] = ' '
					j += 2
					continue
				}
				break
			}
			if buf[j] != '\n' && buf[j] != '\r' && buf[j] != '\t' {
				buf[j] = ' '
			}
			j++
		}
		i = j
	}
	return string(buf)
}

// splitTopLevelDefs splits a CREATE TABLE body on paren-depth-0 commas, so commas
// nested in parens (DECIMAL(10,2), composite PK) stay within one def. Splits run over
// the string-blanked copy; returned slices come from raw body so DEFAULT survives verbatim.
func splitTopLevelDefs(body string) []string {
	blanked := blankStringLiterals(body) // length-preserving → byte indices align with body
	var defs []string
	depth := 0
	start := 0
	for i, r := range blanked {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if def := strings.TrimSpace(body[start:i]); def != "" {
					defs = append(defs, def)
				}
				start = i + 1
			}
		}
	}
	if def := strings.TrimSpace(body[start:]); def != "" {
		defs = append(defs, def)
	}
	return defs
}

func parseTable(tableName, body string, d dialect.Dialect) sqlTable {
	table := sqlTable{
		Name:      tableName,
		GoName:    toCamelCase(tableName),
		TableName: tableName,
	}

	var tableLevelPKCols []string
	var tableLevelUniqueCols []string

	for _, def := range splitTopLevelDefs(body) {
		// columnRegex expects single-line input; flatten a multi-line DEFAULT/CHECK def.
		line := strings.NewReplacer("\n", " ", "\r", " ").Replace(def)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		upperLine := strings.ToUpper(line)

		if strings.HasPrefix(upperLine, "FOREIGN KEY") {
			continue
		}

		if strings.HasPrefix(upperLine, "PRIMARY KEY") {
			if m := tablePKRegex.FindStringSubmatch(line); len(m) >= 2 {
				tableLevelPKCols = splitColList(m[1])
			}
			continue
		}

		// Only single-column UNIQUE drives FindOneByX. Composite UNIQUE skipped.
		if strings.HasPrefix(upperLine, "UNIQUE") {
			if m := tableUniqueRegex.FindStringSubmatch(line); len(m) >= 2 {
				cols := splitColList(m[1])
				if len(cols) == 1 {
					tableLevelUniqueCols = append(tableLevelUniqueCols, cols[0])
				}
			}
			continue
		}

		if strings.HasPrefix(upperLine, "CONSTRAINT") || strings.HasPrefix(upperLine, "CHECK") {
			continue
		}

		col, ok := parseColumn(line, d)
		if !ok {
			continue
		}
		if col.GoType == "time.Time" || col.SQLType == "DATETIME" || col.SQLType == "TIMESTAMP" {
			table.HasTime = true
		}
		if strings.HasPrefix(col.GoType, "sql.") {
			table.HasNullable = true
		}
		if col.Name == "deleted_at" {
			table.HasDeletedAt = true
		}
		table.Columns = append(table.Columns, col)
	}

	if len(tableLevelPKCols) > 0 {
		applyTableLevelPK(&table, tableLevelPKCols)
	}
	finalisePK(&table)

	for _, name := range tableLevelUniqueCols {
		if c := findColumnByName(&table, name); c != nil {
			c.IsUnique = true
		}
	}
	for _, c := range table.Columns {
		if c.IsUnique && !c.IsPrimary {
			table.UniqueColumns = append(table.UniqueColumns, c)
		}
	}

	return table
}

func parseColumn(line string, d dialect.Dialect) (sqlColumn, bool) {
	m := columnRegex.FindStringSubmatch(line)
	if len(m) < 3 {
		return sqlColumn{}, false
	}
	colName := unquoteIdent(m[1])
	colType := strings.ToUpper(m[2])
	restOriginal := m[3]
	// Blank literals first so a benign DEFAULT 'PRIMARY KEY' / ' UNIQUE ' literal
	// can't false-trigger IsPrimary/IsUnique; extractDefaultValue re-locates from restOriginal.
	rest := strings.ToUpper(blankStringLiterals(restOriginal))

	goName := goFieldFromColumn(colName)
	col := sqlColumn{
		Name:      colName,
		GoName:    goName,
		ParamName: lowerFirstRune(goName),
		SQLType:   colType,
		JSONTag:   colName,
	}

	if strings.Contains(rest, "PRIMARY KEY") {
		col.IsPrimary = true
	}
	if strings.Contains(rest, "AUTOINCREMENT") || strings.Contains(rest, "AUTO_INCREMENT") {
		col.IsAuto = true
	}
	if strings.Contains(colType, "SERIAL") { // Postgres SERIAL/BIGSERIAL are DB-assigned
		col.IsAuto = true
	}
	if strings.Contains(rest, "NOT NULL") {
		col.IsNotNull = true
	}
	// Whole-word so a named constraint like `CONSTRAINT uniquename` doesn't false-trigger.
	if indexKeyword(rest, "UNIQUE") >= 0 {
		col.IsUnique = true
	}
	// Whole-word so `is_default` doesn't false-trigger.
	if indexKeyword(strings.ToUpper(blankStringLiterals(restOriginal)), "DEFAULT") >= 0 {
		col.DefaultValue = extractDefaultValue(restOriginal)
	}

	nullable := !col.IsNotNull && !col.IsPrimary

	// created_at/updated_at forced non-null time.Time so the Insert hook can call
	// .IsZero(). deleted_at stays nullable — soft-delete uses NULL = "not deleted".
	switch strings.ToLower(colName) {
	case "created_at", "updated_at":
		nullable = false
	}

	col.GoType = d.MapType(colType, nullable).Name
	return col, true
}

func applyTableLevelPK(t *sqlTable, names []string) {
	for _, raw := range names {
		if c := findColumnByName(t, raw); c != nil {
			c.IsPrimary = true
		}
	}
}

// finalisePK falls back to (ID, int64) when no PK is declared —
// validateTableForCodegen catches the genuinely missing case.
func finalisePK(t *sqlTable) {
	for _, c := range t.Columns {
		if c.IsPrimary {
			t.PKGoFields = append(t.PKGoFields, c.GoName)
			t.PKSqlNames = append(t.PKSqlNames, c.Name)
		}
	}
	if len(t.PKGoFields) > 0 {
		t.PKGoName = t.PKGoFields[0]
		if first := findColumnByName(t, t.PKSqlNames[0]); first != nil {
			t.PKGoType = first.GoType
		}
	}
	if t.PKGoType == "" {
		t.PKGoType = "int64"
		t.PKGoName = "ID"
	}
}

func findColumnByName(t *sqlTable, name string) *sqlColumn {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}
