package runner

import (
	"strings"

	"github.com/tradalab/scorix/cli/runner/dialect"
)

// tableSQL holds the precomputed SQL strings + field lists the model template
// emits. Building these once at codegen time keeps generated files free of
// runtime fmt.Sprintf — every query is a plain const that greps and EXPLAINs.
type tableSQL struct {
	FindOneSQL      string
	FindAllSQL      string
	FindManyBaseSQL string // contains literal `?` for IN clause — sqlx.In + Rebind handle dialect at runtime

	InsertSQL    string
	InsertFields []sqlColumn

	UpdateSQL    string
	UpdateFields []sqlColumn // non-PK, non-created_at columns (PK appended last as arg)

	DeleteSQL    string
	DeleteIsSoft bool // when true, first arg is time.Now() for the deleted_at update

	FindOneByCols []findOneBy

	NeedsTimeImport bool
	NeedsUUIDHook   bool
	HasCreatedAt    bool
	HasUpdatedAt    bool
}

type findOneBy struct {
	GoName    string
	ParamName string
	GoType    string
	SQL       string
}

func buildSQL(t sqlTable, d dialect.Dialect) tableSQL {
	sql := tableSQL{}
	q := d.Quote
	tableQ := q(t.TableName)

	allCols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		allCols[i] = q(c.Name)
	}
	colList := strings.Join(allCols, ",")

	softDeleteWhere := ""
	softDeleteWhereStandalone := ""
	if t.HasDeletedAt {
		softDeleteWhere = " AND " + q("deleted_at") + " IS NULL"
		softDeleteWhereStandalone = " WHERE " + q("deleted_at") + " IS NULL"
	}

	pkCol := q(t.PKSqlNames[0])

	sql.FindOneSQL = "SELECT " + colList + " FROM " + tableQ +
		" WHERE " + pkCol + " = " + d.Placeholder(1) + softDeleteWhere + " LIMIT 1"

	sql.FindAllSQL = "SELECT " + colList + " FROM " + tableQ + softDeleteWhereStandalone

	// sqlx.In requires literal `?` regardless of dialect — Rebind converts to
	// $N at runtime for Postgres.
	whereIn := " WHERE " + pkCol + " IN (?)"
	if t.HasDeletedAt {
		whereIn += " AND " + q("deleted_at") + " IS NULL"
	}
	sql.FindManyBaseSQL = "SELECT " + colList + " FROM " + tableQ + whereIn

	sql.InsertFields = append(sql.InsertFields, t.Columns...)
	insertPHs := make([]string, len(t.Columns))
	for i := range t.Columns {
		insertPHs[i] = d.Placeholder(i + 1)
	}
	sql.InsertSQL = "INSERT INTO " + tableQ + " (" + colList + ") VALUES (" + strings.Join(insertPHs, ",") + ")"

	var setExpr []string
	pos := 0
	for _, c := range t.Columns {
		if c.IsPrimary {
			continue
		}
		if c.Name == "created_at" {
			sql.HasCreatedAt = true
			continue
		}
		if c.Name == "updated_at" {
			sql.HasUpdatedAt = true
		}
		pos++
		setExpr = append(setExpr, q(c.Name)+" = "+d.Placeholder(pos))
		sql.UpdateFields = append(sql.UpdateFields, c)
	}
	pos++
	sql.UpdateSQL = "UPDATE " + tableQ + " SET " + strings.Join(setExpr, ", ") +
		" WHERE " + pkCol + " = " + d.Placeholder(pos)

	if t.HasDeletedAt {
		sql.DeleteIsSoft = true
		sql.DeleteSQL = "UPDATE " + tableQ + " SET " + q("deleted_at") + " = " + d.Placeholder(1) +
			" WHERE " + pkCol + " = " + d.Placeholder(2)
	} else {
		sql.DeleteSQL = "DELETE FROM " + tableQ + " WHERE " + pkCol + " = " + d.Placeholder(1)
	}

	for _, c := range t.UniqueColumns {
		sql.FindOneByCols = append(sql.FindOneByCols, findOneBy{
			GoName:    c.GoName,
			ParamName: c.ParamName,
			GoType:    c.GoType,
			SQL: "SELECT " + colList + " FROM " + tableQ +
				" WHERE " + q(c.Name) + " = " + d.Placeholder(1) + softDeleteWhere + " LIMIT 1",
		})
	}

	sql.NeedsTimeImport = t.HasTime || sql.HasCreatedAt || sql.HasUpdatedAt
	sql.NeedsUUIDHook = looksLikeUUIDDefault(t)

	return sql
}

// looksLikeUUIDDefault detects schemas whose PK relies on a SQL-side UUID
// expression (randomblob, hex, uuid). When true the generator adds a Go-side
// `if data.PK == "" { uuid.NewString() }` fallback in Insert.
func looksLikeUUIDDefault(t sqlTable) bool {
	if len(t.PKSqlNames) == 0 {
		return false
	}
	pk := findColumnByName(&t, t.PKSqlNames[0])
	if pk == nil || pk.GoType != "string" || pk.DefaultValue == "" {
		return false
	}
	lower := strings.ToLower(pk.DefaultValue)
	for _, marker := range []string{"randomblob", "uuid", "hex("} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
