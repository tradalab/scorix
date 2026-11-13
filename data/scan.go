package data

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	scannerType = reflect.TypeFor[sql.Scanner]()
	timeType    = reflect.TypeFor[time.Time]()
	plans       sync.Map
)

func scalar(t reflect.Type) bool {
	return t.Kind() != reflect.Struct || t == timeType || reflect.PointerTo(t).Implements(scannerType)
}

func embedded(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || scalar(t) {
		return nil
	}
	return t
}

func columnsOf(t reflect.Type) (map[string][]int, error) {
	if p, ok := plans.Load(t); ok {
		return p.(map[string][]int), nil
	}
	out := map[string][]int{}
	open := map[reflect.Type]bool{}
	var walk func(t reflect.Type, index []int) error
	walk = func(t reflect.Type, index []int) error {
		if open[t] {
			return fmt.Errorf("data: %s embeds itself, so a row has no end; embed a different type", t)
		}
		open[t] = true
		defer delete(open, t)
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			at := append(append([]int(nil), index...), i)
			name, _, _ := strings.Cut(f.Tag.Get("db"), ",")
			switch {
			case name == "-":
			case name == "" && f.Anonymous && embedded(f.Type) != nil:
				if f.Type.Kind() == reflect.Pointer && !f.IsExported() {
					return fmt.Errorf("data: %s embeds %s, which is unexported, so a scan cannot allocate it; export it or embed it by value", t, f.Type)
				}
				if err := walk(embedded(f.Type), at); err != nil {
					return err
				}
			case name == "":
			case !f.IsExported():
				return fmt.Errorf("data: %s.%s has db tag %q but is unexported", t, f.Name, name)
			default:
				if _, dup := out[name]; dup {
					return fmt.Errorf("data: %s names column %q twice", t, name)
				}
				out[name] = at
			}
		}
		return nil
	}
	if err := walk(t, nil); err != nil {
		return nil, err
	}
	plans.Store(t, out)
	return out, nil
}

type plan struct {
	scalar bool
	fields [][]int
}

func planFor(rows *sql.Rows, t reflect.Type) (plan, error) {
	cols, err := rows.Columns()
	if err != nil {
		return plan{}, err
	}
	if scalar(t) {
		if len(cols) != 1 {
			return plan{}, fmt.Errorf("data: the query returns %d columns into one %s", len(cols), t)
		}
		return plan{scalar: true}, nil
	}
	byName, err := columnsOf(t)
	if err != nil {
		return plan{}, err
	}
	p := plan{fields: make([][]int, len(cols))}
	seen := map[string]bool{}
	var extra []string
	for i, c := range cols {
		idx, ok := byName[c]
		if !ok {
			extra = append(extra, c)
			continue
		}
		p.fields[i], seen[c] = idx, true
	}
	var missing []string
	for name := range byName {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	switch {
	case len(extra) > 0:
		return plan{}, fmt.Errorf("data: %s has no db field for column %s", t, strings.Join(extra, ", "))
	case len(missing) > 0:
		return plan{}, fmt.Errorf("data: the query selects no column for %s field %s", t, strings.Join(missing, ", "))
	}
	return p, nil
}

func (p plan) scan(rows *sql.Rows, v reflect.Value) error {
	if p.scalar {
		return rows.Scan(v.Addr().Interface())
	}
	dests := make([]any, len(p.fields))
	for i, idx := range p.fields {
		dests[i] = fieldAt(v, idx).Addr().Interface()
	}
	return rows.Scan(dests...)
}

func fieldAt(v reflect.Value, index []int) reflect.Value {
	for _, i := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v
}

func target(dst any, slice bool) (reflect.Value, error) {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return reflect.Value{}, fmt.Errorf("data: dst must be a non-nil pointer, got %T", dst)
	}
	v = v.Elem()
	if slice && v.Kind() != reflect.Slice {
		return reflect.Value{}, fmt.Errorf("data: QueryRowsCtx needs a pointer to a slice, got %T", dst)
	}
	return v, nil
}

func scanOne(rows *sql.Rows, dst any) error {
	v, err := target(dst, false)
	if err != nil {
		return err
	}
	p, err := planFor(rows, v.Type())
	if err != nil {
		return err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return ErrNotFound
	}
	if err := p.scan(rows, v); err != nil {
		return err
	}
	if rows.Next() {
		return errors.New("data: QueryRowCtx matched more than one row; narrow the query or use QueryRowsCtx")
	}
	return rows.Err()
}

func scanAll(rows *sql.Rows, dst any) error {
	v, err := target(dst, true)
	if err != nil {
		return err
	}
	elem := v.Type().Elem()
	pointers := elem.Kind() == reflect.Pointer
	if pointers {
		elem = elem.Elem()
	}
	p, err := planFor(rows, elem)
	if err != nil {
		return err
	}
	out := reflect.MakeSlice(v.Type(), 0, 0)
	for rows.Next() {
		item := reflect.New(elem)
		if err := p.scan(rows, item.Elem()); err != nil {
			return err
		}
		if pointers {
			out = reflect.Append(out, item)
		} else {
			out = reflect.Append(out, item.Elem())
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	v.Set(out)
	return nil
}
