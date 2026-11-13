package pagetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/tradalab/scorix/data/page"
	"github.com/tradalab/scorix/fault"
)

const listProto = `syntax = "proto3";
package acme;
message Row { int64 id = 1; string group = 2; }
message ListReq { int32 page_size = 1; string page_token = 2; bool want_total = 3; string group = 4; }
message ListRes { repeated Row rows = 1; string next_page_token = 2; int32 total_size = 3; repeated string notes = 4; }
`

type broken struct {
	noLookahead, noMax, inclusive, totalAlways, totalLater, unbound, negativeOK, extraRow bool
	filterLate, endless, defaultIsMax, lenientToken, countAll, skipRow                    bool
	asGRPC                                                                                bool
}

type fixture struct {
	req, res, row protoreflect.MessageDescriptor
	spec          page.Spec
}

func compile(t *testing.T) fixture {
	t.Helper()
	files, err := (&protocompile.Compiler{Resolver: &protocompile.SourceResolver{
		Accessor: protocompile.SourceAccessorFromMap(map[string]string{"list.proto": listProto}),
	}}).Compile(context.Background(), "list.proto")
	if err != nil {
		t.Fatal(err)
	}
	ms := files[0].Messages()
	return fixture{req: ms.ByName("ListReq"), res: ms.ByName("ListRes"), row: ms.ByName("Row"),
		spec: page.Spec{Default: 2, Max: 4, Order: []page.Order{{Column: "id", Desc: true}}}}
}

func (f fixture) lister(rows []int64, groups map[int64]string, b broken) func(context.Context, *dynamicpb.Message) (*dynamicpb.Message, error) {
	return func(_ context.Context, in *dynamicpb.Message) (*dynamicpb.Message, error) {
		get := func(name string) protoreflect.Value { return in.Get(f.req.Fields().ByName(protoreflect.Name(name))) }
		size := int32(get("page_size").Int())
		if b.negativeOK && size < 0 {
			size = 0
		}
		if b.defaultIsMax && size == 0 {
			size = f.spec.Max
		}
		if b.noMax && size > f.spec.Max {
			f.spec.Max = size
		}
		group := get("group").String()
		filter := any(group)
		if b.unbound {
			filter = nil
		}
		tok := get("page_token").String()
		if b.lenientToken && tok == "not-a-token" {
			tok = ""
		}
		p, err := f.spec.Start(size, tok, filter)
		if err != nil && b.asGRPC {
			return nil, fmt.Errorf("list: %w", grpcErr{&grpcStatus{code: 3}})
		}
		if err != nil {
			return nil, err
		}
		var match []int64
		for _, id := range rows {
			if groups[id] == group || b.filterLate {
				match = append(match, id)
			}
		}
		slices.Sort(match)
		slices.Reverse(match)
		var after []int64
		args := p.Args()
		for _, id := range match {
			if b.skipRow && id == 5 {
				continue
			}
			if len(args) == 0 {
				after = append(after, id)
				continue
			}
			var cur int64
			fmt.Sscan(fmt.Sprint(args[0]), &cur)
			if id < cur || (b.inclusive && id == cur) {
				after = append(after, id)
			}
		}
		fetch := p.Fetch()
		if b.noLookahead || b.skipRow {
			fetch = p.Size
		}
		got := after[:min(len(after), fetch)]
		var keep int
		var next string
		if len(got) > 0 {
			keep, next, err = p.Next(len(got), got[min(len(got), p.Size)-1])
			if err != nil {
				return nil, err
			}
		}
		if (b.noLookahead || b.skipRow) && len(got) == p.Size && len(after) >= p.Size {
			keep, next, _ = p.Next(p.Size+1, got[p.Size-1])
		}
		if b.extraRow && keep < len(got) {
			keep++
		}
		if b.endless && next == "" {
			next = get("page_token").String()
		}
		kept := got[:keep]
		if b.filterLate {
			kept = slices.DeleteFunc(slices.Clone(kept), func(id int64) bool { return groups[id] != group })
		}
		out := f.reply(kept, next)
		want := get("want_total").Bool()
		total := len(match)
		if b.filterLate {
			total = 6
		}
		if b.countAll {
			total = len(rows)
		}
		if p.CountTotal(want) || b.totalAlways || (b.totalLater && want) {
			out.Set(f.res.Fields().ByName("total_size"), protoreflect.ValueOfInt32(int32(total)))
		}
		return out, nil
	}
}

func (f fixture) reply(ids []int64, next string) *dynamicpb.Message {
	out := dynamicpb.NewMessage(f.res)
	list := out.Mutable(f.res.Fields().ByName("rows")).List()
	for _, id := range ids {
		r := dynamicpb.NewMessage(f.row)
		r.Set(f.row.Fields().ByName("id"), protoreflect.ValueOfInt64(id))
		list.Append(protoreflect.ValueOfMessage(r))
	}
	out.Mutable(f.res.Fields().ByName("notes")).List().Append(protoreflect.ValueOfString("a note"))
	out.Set(f.res.Fields().ByName("next_page_token"), protoreflect.ValueOfString(next))
	return out
}

type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (f fixture) check(t *testing.T, b broken) []string {
	t.Helper()
	rows := []int64{1, 2, 3, 4, 5, 6, 7}
	groups := map[int64]string{1: "a", 2: "a", 3: "b", 4: "a", 5: "a", 6: "a", 7: "a"}
	fn := f.lister(rows, groups, b)
	base := func(group string) *dynamicpb.Message {
		m := dynamicpb.NewMessage(f.req)
		m.Set(f.req.Fields().ByName("group"), protoreflect.ValueOfString(group))
		return m
	}
	r := &recorder{TB: t}
	Check(r, List{Spec: f.spec, Rows: 6, Total: true, Call: Of(fn, base("a")), Other: Of(fn, base("b"))})
	return r.errs
}

func TestACorrectListPassesEveryRule(t *testing.T) {
	if errs := compile(t).check(t, broken{}); len(errs) != 0 {
		t.Fatalf("a correct list failed:\n%s", strings.Join(errs, "\n"))
	}
}

func TestEachBrokenRuleIsReported(t *testing.T) {
	f := compile(t)
	for name, c := range map[string]struct {
		b    broken
		want string
	}{
		"no lookahead":     {broken{noLookahead: true}, "6 rows in 7 pages"},
		"no max":           {broken{noMax: true}, "over max_page_size"},
		"inclusive cursor": {broken{inclusive: true}, "repeats a row"},
		"total always":     {broken{totalAlways: true}, "without want_total"},
		"total later":      {broken{totalLater: true}, "on a later page"},
		"unbound token":    {broken{unbound: true}, "under another filter"},
		"negative size":    {broken{negativeOK: true}, "negative page_size"},
		"extra row":        {broken{extraRow: true}, "holds 2 rows"},
		"filter after":     {broken{filterLate: true}, "rows are filtered after the LIMIT"},
		"endless":          {broken{endless: true}, "is still set"},
		"default is max":   {broken{defaultIsMax: true}, "page_size 0 gave 4 rows"},
		"lenient token":    {broken{lenientToken: true}, "never issued"},
		"count all":        {broken{countAll: true}, "on the first page = 7"},
		"skipped row":      {broken{skipRow: true}, "saw 5 rows in 6 pages"},
	} {
		errs := f.check(t, c.b)
		if !strings.Contains(strings.Join(errs, "\n"), c.want) {
			t.Errorf("%s: reported %q, want a line with %q", name, errs, c.want)
		}
	}
}

func TestTooFewRowsIsReportedRatherThanPassed(t *testing.T) {
	r := &recorder{TB: t}
	Check(r, List{Rows: 2})
	if len(r.errs) != 1 || !strings.Contains(r.errs[0], "at least 3 rows") {
		t.Fatalf("errs = %q", r.errs)
	}
}

type grpcCode uint32

type grpcStatus struct{ code grpcCode }

func (s *grpcStatus) Code() grpcCode { return s.code }

type grpcErr struct{ s *grpcStatus }

func (e grpcErr) Error() string           { return "rpc error" }
func (e grpcErr) GRPCStatus() *grpcStatus { return e.s }

type textStatus struct{}

func (textStatus) Code() string { return "invalid_argument" }

type textErr struct{}

func (textErr) Error() string          { return "text code" }
func (textErr) GRPCStatus() textStatus { return textStatus{} }

type wrongShape struct{}

func (wrongShape) Error() string           { return "wrong shape" }
func (wrongShape) GRPCStatus(int) grpcCode { return 3 }

func TestAListThatRefusesWithAGRPCStatusPasses(t *testing.T) {
	if errs := compile(t).check(t, broken{asGRPC: true}); len(errs) != 0 {
		t.Fatalf("a list answering InvalidArgument as a gRPC status failed:\n%s", strings.Join(errs, "\n"))
	}
}

func TestAnyErrorCarryingInvalidArgumentCountsAsInvalid(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"fault":               {fault.New(fault.CodeInvalid, "bad token"), true},
		"connect":             {connect.NewError(connect.CodeInvalidArgument, errors.New("bad")), true},
		"grpc status":         {grpcErr{&grpcStatus{code: 3}}, true},
		"wrapped grpc status": {fmt.Errorf("list: %w", grpcErr{&grpcStatus{code: 3}}), true},
		"joined grpc status":  {errors.Join(errors.New("x"), grpcErr{&grpcStatus{code: 3}}), true},
		"grpc not found":      {grpcErr{&grpcStatus{code: 5}}, false},
		"grpc nil status":     {grpcErr{nil}, false},
		"connect not found":   {connect.NewError(connect.CodeNotFound, errors.New("x")), false},
		"wrong method shape":  {wrongShape{}, false},
		"plain":               {errors.New("bad token"), false},
		"nil":                 {nil, false},
	} {
		if got := invalid(c.err); got != c.want {
			t.Errorf("%s: invalid = %v, want %v", name, got, c.want)
		}
	}
}
