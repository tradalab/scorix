// Package page pages a list by keyset, the way AIP-158 and the paging skill ask: a size the
// server bounds, an opaque token that names the last row seen and the filter it was cut
// under, and a boundary over every sort column so no row is skipped or seen twice.
package page

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tradalab/scorix/fault"
)

type Order struct {
	Column string
	Desc   bool
}

// Spec is one list's contract, generated from its (scorix.v1.list) option. Order columns
// must be NOT NULL and the last one unique, or rows can fall between two pages; Start
// refuses a Spec unless 0 < Default <= Max.
type Spec struct {
	Default int32
	Max     int32
	Order   []Order
}

// Page is one request against a Spec.
type Page struct {
	spec   Spec
	Size   int
	filter string
	cursor []json.RawMessage
}

func (s Spec) fingerprint(filter any) (string, error) {
	b, err := json.Marshal(filter)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(s.OrderBy()+"|"), b...))
	return hex.EncodeToString(sum[:8]), nil
}

type token struct {
	F string            `json:"f"`
	K []json.RawMessage `json:"k"`
}

// Start reads a request: size 0 is Default, over Max is Max, negative is refused; token is
// empty for the first page, and is refused when it was cut under another filter.
func (s Spec) Start(size int32, tok string, filter any) (Page, error) {
	if s.Default <= 0 || s.Default > s.Max {
		return Page{}, fmt.Errorf("page: Spec Default %d and Max %d need 0 < Default <= Max", s.Default, s.Max)
	}
	switch {
	case size < 0:
		return Page{}, fault.Errorf(fault.CodeInvalid, "page_size %d is negative", size)
	case size == 0:
		size = s.Default
	case size > s.Max:
		size = s.Max
	}
	fp, err := s.fingerprint(filter)
	if err != nil {
		return Page{}, err
	}
	p := Page{spec: s, Size: int(size), filter: fp}
	if tok == "" {
		return p, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	var t token
	if err == nil {
		err = json.Unmarshal(raw, &t)
	}
	switch {
	case err != nil || len(t.K) != len(s.Order) || !scalars(t.K):
		return Page{}, fault.New(fault.CodeInvalid, "page_token is not one this list issued")
	case t.F != fp:
		return Page{}, fault.New(fault.CodeInvalid, "page_token was issued for another filter or sort; start again without it")
	}
	p.cursor = t.K
	return p, nil
}

func scalars(values []json.RawMessage) bool {
	for _, raw := range values {
		switch decode(raw).(type) {
		case string, json.Number, bool:
		default:
			return false
		}
	}
	return true
}

func decode(raw json.RawMessage) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	_ = dec.Decode(&v)
	return v
}

func placeholder(n int) string { return fmt.Sprintf("$%d", n) }

// CountTotal says whether to run the count query: only when the caller asked, and only on
// the first page, since a count cannot change what a reader already scrolled past.
func (p Page) CountTotal(want bool) bool { return want && len(p.cursor) == 0 }

// Fetch is how many rows to ask for: one past Size, so Next can tell a full page from the end.
func (p Page) Fetch() int { return p.Size + 1 }

// Args are the cursor's values, to append after the query's own arguments.
func (p Page) Args() []any {
	out := make([]any, len(p.cursor))
	for i, raw := range p.cursor {
		out[i] = decode(raw)
	}
	return out
}

// Boundary keeps the rows after the cursor, with $n placeholders numbered from first; on
// the first page it is TRUE and Args is empty.
func (p Page) Boundary(first int) string {
	if len(p.cursor) == 0 {
		return "TRUE"
	}
	var ors []string
	for i, o := range p.spec.Order {
		var ands []string
		for j := 0; j < i; j++ {
			ands = append(ands, p.spec.Order[j].Column+" = "+placeholder(first+j))
		}
		cmp := " > "
		if o.Desc {
			cmp = " < "
		}
		ands = append(ands, o.Column+cmp+placeholder(first+i))
		ors = append(ors, "("+strings.Join(ands, " AND ")+")")
	}
	return "(" + strings.Join(ors, " OR ") + ")"
}

// OrderBy is the ORDER BY list that matches Boundary.
func (p Page) OrderBy() string { return p.spec.OrderBy() }

func (s Spec) OrderBy() string {
	parts := make([]string, len(s.Order))
	for i, o := range s.Order {
		parts[i] = o.Column + " ASC"
		if o.Desc {
			parts[i] = o.Column + " DESC"
		}
	}
	return strings.Join(parts, ", ")
}

// Next takes how many rows came back and the sort values of the row at index Size-1, and
// says how many to keep and the token for the next page, empty when this was the last. A nil
// sort value is an error naming its column.
func (p Page) Next(got int, last ...any) (keep int, next string, err error) {
	if got <= p.Size {
		return got, "", nil
	}
	if len(last) != len(p.spec.Order) {
		return 0, "", fmt.Errorf("page: Next takes %d sort values, got %d", len(p.spec.Order), len(last))
	}
	t := token{F: p.filter}
	for i, v := range last {
		b, err := json.Marshal(v)
		if err != nil {
			return 0, "", err
		}
		if string(b) == "null" {
			return 0, "", fmt.Errorf("page: sort column %s is NULL on the last row; sort columns must be NOT NULL", p.spec.Order[i].Column)
		}
		t.K = append(t.K, b)
	}
	b, _ := json.Marshal(t)
	return p.Size, base64.RawURLEncoding.EncodeToString(b), nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Contains is q as a LIKE pattern matching it anywhere, with q's own % _ and \ taken
// literally under LIKE's default backslash escape.
func Contains(q string) string { return "%" + likeEscaper.Replace(q) + "%" }
