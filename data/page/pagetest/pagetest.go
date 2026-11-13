// Package pagetest checks that a list rpc keeps the AIP-158 contract data/page promises: every
// row once across pages, sizes bounded by its Spec, tokens bound to their filter, and a total
// counted only when asked on the first page.
package pagetest

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tradalab/scorix/data/page"
	"github.com/tradalab/scorix/fault"
)

// Page is one reply seen through its paging fields; Items are the rows, compared by value.
type Page struct {
	Items []string
	Next  string
	Total int32
}

// Call asks one page with this size, token and want_total.
type Call func(ctx context.Context, size int32, token string, wantTotal bool) (Page, error)

func field(m protoreflect.Message, name string) protoreflect.FieldDescriptor {
	return m.Descriptor().Fields().ByName(protoreflect.Name(name))
}

// Of sees fn through page_size, page_token and want_total on a copy of base, which carries the
// filter, and next_page_token, total_size and the one repeated message field on the reply.
func Of[Req, Res any](fn func(context.Context, *Req) (*Res, error), base *Req) Call {
	return func(ctx context.Context, size int32, token string, wantTotal bool) (Page, error) {
		in := proto.Clone(any(base).(proto.Message))
		m := in.ProtoReflect()
		m.Set(field(m, "page_size"), protoreflect.ValueOfInt32(size))
		m.Set(field(m, "page_token"), protoreflect.ValueOfString(token))
		if f := field(m, "want_total"); f != nil {
			m.Set(f, protoreflect.ValueOfBool(wantTotal))
		}
		res, err := fn(ctx, any(in).(*Req))
		if err != nil {
			return Page{}, err
		}
		out := any(res).(proto.Message).ProtoReflect()
		var p Page
		p.Next = out.Get(field(out, "next_page_token")).String()
		if f := field(out, "total_size"); f != nil {
			p.Total = int32(out.Get(f).Int())
		}
		fields := out.Descriptor().Fields()
		for i := range fields.Len() {
			f := fields.Get(i)
			if !f.IsList() || f.Kind() != protoreflect.MessageKind {
				continue
			}
			list := out.Get(f).List()
			for j := range list.Len() {
				b, err := proto.MarshalOptions{Deterministic: true}.Marshal(list.Get(j).Message().Interface())
				if err != nil {
					return Page{}, err
				}
				p.Items = append(p.Items, string(b))
			}
		}
		return p, nil
	}
}

// List is the list under test: Rows is how many rows the seeded data and Call's filter let
// through, Total says the list has want_total, and Other, when set, is the same list under a
// different filter, where Call's tokens must be refused.
type List struct {
	Spec  page.Spec
	Rows  int
	Total bool
	Call  Call
	Other Call
}

func invalid(err error) bool {
	return fault.CodeOf(err) == fault.CodeInvalid || connect.CodeOf(err) == connect.CodeInvalidArgument || grpcInvalid(err)
}

const grpcInvalidArgument = 3

func grpcInvalid(err error) bool {
	if code, ok := statusCode(err); ok {
		return code == grpcInvalidArgument
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return grpcInvalid(u.Unwrap())
	case interface{ Unwrap() []error }:
		return slices.ContainsFunc(u.Unwrap(), grpcInvalid)
	}
	return false
}

func statusCode(err error) (code uint64, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	status := reflect.ValueOf(err).MethodByName("GRPCStatus").Call(nil)[0]
	return status.MethodByName("Code").Call(nil)[0].Uint(), true
}

// Check runs every rule against l and reports each broken one through t.Errorf. It needs at
// least three rows, so a page boundary falls between rows that both exist.
func Check(t testing.TB, l List) {
	t.Helper()
	ctx := context.Background()
	if l.Rows < 3 {
		t.Errorf("pagetest: seed at least 3 rows, not %d", l.Rows)
		return
	}
	seen := map[string]bool{}
	token, calls := "", 0
	for {
		p, err := l.Call(ctx, 1, token, false)
		if err != nil {
			t.Errorf("pagetest: page %d of size 1: %v", calls+1, err)
			return
		}
		calls++
		if len(p.Items) > 1 {
			t.Errorf("pagetest: page %d of size 1 holds %d rows", calls, len(p.Items))
		}
		if len(p.Items) == 0 && p.Next != "" {
			t.Errorf("pagetest: page %d is empty yet has next_page_token; rows are filtered after the LIMIT", calls)
		}
		for _, it := range p.Items {
			if seen[it] {
				t.Errorf("pagetest: page %d repeats a row an earlier page held", calls)
			}
			seen[it] = true
		}
		if p.Next == "" {
			break
		}
		if calls > l.Rows {
			t.Errorf("pagetest: %d pages of size 1 for %d rows and next_page_token is still set", calls, l.Rows)
			return
		}
		token = p.Next
	}
	if len(seen) != l.Rows || calls != l.Rows {
		t.Errorf("pagetest: walking pages of size 1 saw %d rows in %d pages, want %d in %d", len(seen), calls, l.Rows, l.Rows)
	}
	for _, c := range []struct {
		name string
		size int32
		want int
	}{
		{"page_size 0", 0, min(l.Rows, int(l.Spec.Default))},
		{"a page_size over max_page_size", l.Spec.Max + 1, min(l.Rows, int(l.Spec.Max))},
	} {
		p, err := l.Call(ctx, c.size, "", false)
		if err != nil || len(p.Items) != c.want {
			t.Errorf("pagetest: %s gave %d rows, %v; want %d", c.name, len(p.Items), err, c.want)
		}
	}
	if _, err := l.Call(ctx, -1, "", false); !invalid(err) {
		t.Errorf("pagetest: a negative page_size = %v, want invalid argument", err)
	}
	if _, err := l.Call(ctx, 1, "not-a-token", false); !invalid(err) {
		t.Errorf("pagetest: a page_token this list never issued = %v, want invalid argument", err)
	}
	first, err := l.Call(ctx, 1, "", l.Total)
	if err != nil {
		t.Errorf("pagetest: first page: %v", err)
		return
	}
	if l.Total {
		second, err := l.Call(ctx, 1, first.Next, true)
		plain, perr := l.Call(ctx, 1, "", false)
		if first.Total != int32(l.Rows) {
			t.Errorf("pagetest: total_size on the first page = %d, want %d", first.Total, l.Rows)
		}
		if err != nil || second.Total != 0 {
			t.Errorf("pagetest: total_size on a later page = %d, %v; a count belongs to the first page only", second.Total, err)
		}
		if perr != nil || plain.Total != 0 {
			t.Errorf("pagetest: total_size without want_total = %d, %v; counting is opt-in", plain.Total, perr)
		}
	}
	if l.Other != nil {
		if _, err := l.Other(ctx, 1, first.Next, false); !invalid(err) {
			t.Errorf("pagetest: a page_token used under another filter = %v, want invalid argument", err)
		}
	}
}
