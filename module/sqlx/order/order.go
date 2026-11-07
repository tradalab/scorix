// Package order stores a user-chosen order of rows, as drag to reorder in a sidebar does, and the free-form tag
// lists those rows carry. It works on any scorix sqlx connection, SQLite or Postgres.
package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Spec names the table and columns as plain, unqualified identifiers, quoted as written (exact case on Postgres).
// Table and OrderColumn are required; IDColumn defaults to "id"; GroupColumn scopes positions to a group;
// DeletedColumn skips soft-deleted rows; UpdatedColumn is stamped on reorder.
type Spec struct {
	Table         string
	IDColumn      string
	OrderColumn   string
	GroupColumn   string
	DeletedColumn string
	UpdatedColumn string
}

func (s Spec) id() string {
	if s.IDColumn == "" {
		return "id"
	}
	return s.IDColumn
}

func (s Spec) check() error {
	for _, name := range []string{s.Table, s.id(), s.OrderColumn, s.GroupColumn, s.DeletedColumn, s.UpdatedColumn} {
		if strings.ContainsAny(name, "\"`;. \t\n") {
			return fmt.Errorf("order: %q is not a plain identifier", name)
		}
	}
	if s.Table == "" || s.OrderColumn == "" {
		return errors.New("order: Table and OrderColumn are required")
	}
	return nil
}

func q(name string) string { return `"` + name + `"` }

func (s Spec) scope(group string, args []any) (string, []any) {
	var where []string
	if s.GroupColumn != "" {
		where = append(where, q(s.GroupColumn)+" = ?")
		args = append(args, group)
	}
	if s.DeletedColumn != "" {
		where = append(where, q(s.DeletedColumn)+" IS NULL")
	}
	if len(where) == 0 {
		return "", args
	}
	return strings.Join(where, " AND "), args
}

// Next returns the position after the last row of group (0 for an empty group), for a row being added there.
// group is ignored when the Spec has no GroupColumn.
func Next(ctx context.Context, db sqlx.ExtContext, s Spec, group string) (int64, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	query := "SELECT COALESCE(MAX(" + q(s.OrderColumn) + "), -1) + 1 FROM " + q(s.Table)
	where, args := s.scope(group, nil)
	if where != "" {
		query += " WHERE " + where
	}
	var next int64
	err := sqlx.GetContext(ctx, db, &next, db.Rebind(query), args...)
	return next, err
}

// Reorder numbers ids 0..n-1 in the order given and moves them into group (when the Spec has a GroupColumn), in
// one statement of three parameters per id, so keep a call under about 20k ids. Repeated ids keep their first
// place, an empty list does nothing, and ids not listed keep their place.
func Reorder(ctx context.Context, db sqlx.ExtContext, s Spec, group string, ids []string, now time.Time) error {
	if err := s.check(); err != nil {
		return err
	}
	ids = unique(ids)
	if len(ids) == 0 {
		return nil
	}
	var set []string
	var args []any
	if s.GroupColumn != "" {
		set = append(set, q(s.GroupColumn)+" = ?")
		args = append(args, group)
	}
	var b strings.Builder
	b.WriteString(q(s.OrderColumn) + " = CASE " + q(s.id()))
	for i, id := range ids {
		b.WriteString(" WHEN ? THEN CAST(? AS INTEGER)")
		args = append(args, id, i)
	}
	b.WriteString(" END")
	set = append(set, b.String())
	if s.UpdatedColumn != "" {
		set = append(set, q(s.UpdatedColumn)+" = ?")
		args = append(args, now)
	}
	query := "UPDATE " + q(s.Table) + " SET " + strings.Join(set, ", ") +
		" WHERE " + q(s.id()) + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
	for _, id := range ids {
		args = append(args, id)
	}
	if s.DeletedColumn != "" {
		query += " AND " + q(s.DeletedColumn) + " IS NULL"
	}
	_, err := db.ExecContext(ctx, db.Rebind(query), args...)
	return err
}

func unique(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
