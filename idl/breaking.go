package idl

import (
	"cmp"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Level is which callers a Change breaks; each level also breaks every caller of the ones
// above it, so a Wire change breaks JSON and Contract callers too.
type Level int

const (
	// Wire breaks a caller sending protobuf binary: zrpc, gRPC, Connect with proto.
	Wire Level = iota + 1
	// JSON breaks a caller sending JSON: Connect JSON, REST, the generated TS client.
	JSON
	// Contract breaks a caller relying on a scorix.v1 trait: a route, an effect, a permission, paging, an MCP tool.
	Contract
)

func (l Level) String() string {
	return map[Level]string{Wire: "wire", JSON: "json", Contract: "contract"}[l]
}

// ParseLevel reads "wire", "json" or "contract".
func ParseLevel(s string) (Level, error) {
	for _, l := range []Level{Wire, JSON, Contract} {
		if l.String() == s {
			return l, nil
		}
	}
	return 0, fmt.Errorf("level %q: want wire, json or contract", s)
}

// Change is one way the new contract breaks a caller built against the old one.
type Change struct {
	Level Level
	Where string
	What  string
}

func (c Change) String() string { return fmt.Sprintf("%s: %s %s", c.Level, c.Where, c.What) }

// Breaking compiles both versions of the proto file name and lists what after breaks for
// a caller of before: wire and JSON changes, then contract ones, each in before's order.
// A compile error names its side.
func Breaking(name, before, after string) ([]Change, error) {
	oldFD, err := compile(name, before)
	if err != nil {
		return nil, fmt.Errorf("before: %w", err)
	}
	newFD, err := compile(name, after)
	if err != nil {
		return nil, fmt.Errorf("after: %w", err)
	}
	oldFile, err := read(oldFD)
	if err != nil {
		return nil, fmt.Errorf("before: %w", err)
	}
	newFile, err := read(newFD)
	if err != nil {
		return nil, fmt.Errorf("after: %w", err)
	}
	var d diff
	d.descriptors(oldFD, newFD)
	d.contracts(oldFile, newFile)
	return d.changes, nil
}

type diff struct{ changes []Change }

func (d *diff) add(l Level, where, what string, args ...any) {
	d.changes = append(d.changes, Change{Level: l, Where: where, What: fmt.Sprintf(what, args...)})
}

func (d *diff) descriptors(before, after protoreflect.FileDescriptor) {
	if before.Package() != after.Package() {
		d.add(Wire, "file", "package %s is %s now, so every procedure path changes", before.Package(), after.Package())
	}
	for i := 0; i < before.Services().Len(); i++ {
		os := before.Services().Get(i)
		ns := after.Services().ByName(os.Name())
		if ns == nil {
			d.add(Wire, "service "+string(os.Name()), "was removed")
			continue
		}
		for j := 0; j < os.Methods().Len(); j++ {
			d.method(os.Methods().Get(j), ns.Methods().ByName(os.Methods().Get(j).Name()))
		}
	}
	for i := 0; i < before.Messages().Len(); i++ {
		om := before.Messages().Get(i)
		if nm := after.Messages().ByName(om.Name()); nm != nil {
			d.message(om, nm)
		}
	}
}

func (d *diff) method(om, nm protoreflect.MethodDescriptor) {
	where := fmt.Sprintf("rpc %s.%s", om.Parent().Name(), om.Name())
	if nm == nil {
		d.add(Wire, where, "was removed")
		return
	}
	if o, n := local(om.Input()), local(nm.Input()); o != n {
		d.add(renameLevel(om.Input(), nm.Input()), where, "takes %s, was %s", n, o)
	}
	if o, n := local(om.Output()), local(nm.Output()); o != n {
		d.add(renameLevel(om.Output(), nm.Output()), where, "returns %s, was %s", n, o)
	}
	for _, s := range []struct {
		side     string
		old, new bool
	}{{"request", om.IsStreamingClient(), nm.IsStreamingClient()}, {"response", om.IsStreamingServer(), nm.IsStreamingServer()}} {
		switch {
		case !s.old && s.new:
			d.add(Wire, where, "streams its %s now", s.side)
		case s.old && !s.new:
			d.add(Wire, where, "no longer streams its %s", s.side)
		}
	}
}

func renameLevel(om, nm protoreflect.MessageDescriptor) Level {
	if om.Fields().Len() != nm.Fields().Len() {
		return Wire
	}
	for i := 0; i < om.Fields().Len(); i++ {
		of := om.Fields().Get(i)
		nf := nm.Fields().ByNumber(of.Number())
		if nf == nil || of.Cardinality() != nf.Cardinality() || kindName(of) != kindName(nf) {
			return Wire
		}
	}
	return JSON
}

var wireAlike = map[protoreflect.Kind]int{
	protoreflect.Int32Kind: 1, protoreflect.Int64Kind: 1, protoreflect.Uint32Kind: 1, protoreflect.Uint64Kind: 1, protoreflect.BoolKind: 1,
	protoreflect.Sint32Kind: 2, protoreflect.Sint64Kind: 2,
	protoreflect.Fixed32Kind: 3, protoreflect.Sfixed32Kind: 3,
	protoreflect.Fixed64Kind: 4, protoreflect.Sfixed64Kind: 4,
	protoreflect.StringKind: 5, protoreflect.BytesKind: 5,
}

func local(d protoreflect.Descriptor) string {
	return strings.TrimPrefix(string(d.FullName()), string(d.ParentFile().Package())+".")
}

func kindName(f protoreflect.FieldDescriptor) string {
	if f.Kind() == protoreflect.MessageKind {
		return local(f.Message())
	}
	return f.Kind().String()
}

func (d *diff) message(om, nm protoreflect.MessageDescriptor) {
	where := "message " + string(om.Name())
	for i := 0; i < om.Fields().Len(); i++ {
		of := om.Fields().Get(i)
		nf := nm.Fields().ByNumber(of.Number())
		label := fmt.Sprintf("field %d (%s)", of.Number(), of.Name())
		if nf == nil {
			if nm.ReservedRanges().Has(of.Number()) {
				d.add(JSON, where, "%s was removed", label)
			} else {
				d.add(Wire, where, "%s was removed without reserving %d, so a later field can take its number", label, of.Number())
			}
			continue
		}
		if o, n := of.Cardinality() == protoreflect.Repeated, nf.Cardinality() == protoreflect.Repeated; o != n {
			d.add(Wire, where, "%s is %s now", label, map[bool]string{true: "repeated", false: "no longer repeated"}[n])
		}
		if o, n := kindName(of), kindName(nf); o != n {
			level := Wire
			if g := wireAlike[of.Kind()]; g != 0 && g == wireAlike[nf.Kind()] {
				level = JSON
			}
			d.add(level, where, "%s is %s, was %s", label, n, o)
		}
		if of.Name() != nf.Name() {
			d.add(JSON, where, "field %d was renamed %s to %s", of.Number(), of.Name(), nf.Name())
		} else if of.JSONName() != nf.JSONName() {
			d.add(JSON, where, "%s is %s in JSON, was %s", label, nf.JSONName(), of.JSONName())
		}
	}
}

var effectRank = map[string]int{"readonly": 1, "idempotent": 2, "": 3, "mutating": 3, "destructive": 4}

func routeKey(h *HTTP) string {
	return h.Method + " " + paramRe.ReplaceAllString(h.Path, "{}")
}

func messagesByName(f File) map[string]Message {
	out := map[string]Message{}
	for _, m := range f.Messages {
		out[m.Name] = m
	}
	return out
}

func orderText(l *List) string {
	parts := make([]string, len(l.Order))
	for i, o := range l.Order {
		parts[i] = o.Column + map[bool]string{true: " desc", false: " asc"}[o.Desc]
	}
	return strings.Join(parts, ", ")
}

func (d *diff) contracts(before, after File) {
	newMsgs, oldMsgs := messagesByName(after), messagesByName(before)
	newRPCs := map[string]RPC{}
	for _, s := range after.Services {
		for _, r := range s.RPCs {
			newRPCs[s.Name+"."+r.Name] = r
		}
	}
	for _, s := range before.Services {
		for _, o := range s.RPCs {
			n, ok := newRPCs[s.Name+"."+o.Name]
			if !ok {
				continue
			}
			where := "rpc " + s.Name + "." + o.Name
			if d.route(where, o.HTTP, n.HTTP) {
				d.headers(where, oldMsgs[o.RequestType], newMsgs[n.RequestType])
			}
			if effectRank[n.Effect] > effectRank[o.Effect] {
				d.add(Contract, where, "is %s now, was %s, so a caller that retries it or runs it unasked can do harm", effectWord(n.Effect), effectWord(o.Effect))
			}
			if n.Permission != "" && n.Permission != o.Permission {
				d.add(Contract, where, "needs permission %s now%s", n.Permission, was(o.Permission))
			}
			if o.Replay && !n.Replay {
				d.add(Contract, where, "no longer replays a retry that carries its Idempotency-Key, so the retry runs again")
			}
			if o.Audit && !n.Audit {
				d.add(Contract, where, "is no longer audited, so its calls leave no record")
			}
			switch {
			case o.List != nil && n.List == nil:
				d.add(Contract, where, "no longer pages")
			case o.List == nil && n.List != nil:
				d.add(Contract, where, "pages now, so a caller that read every row gets only the first %d", n.List.Default)
			case o.List != nil && orderText(o.List) != orderText(n.List):
				d.add(Contract, where, "orders by %s now, was %s, so a page token issued before resumes at the wrong row", orderText(n.List), orderText(o.List))
			}
			if o.List != nil && n.List != nil && n.List.Max < o.List.Max {
				d.add(Contract, where, "returns at most %d rows a page now, was %d", n.List.Max, o.List.Max)
			}
			switch {
			case o.IsEvent && !n.IsEvent:
				d.add(Contract, where, "is no longer an event")
			case n.IsEvent && !o.IsEvent:
				d.add(Contract, where, "is an %s event now, was a call", n.EventDir)
			case n.IsEvent && o.EventDir != n.EventDir:
				d.add(Contract, where, "is an %s event now, was %s", n.EventDir, o.EventDir)
			}
			if o.MCP && !n.MCP {
				d.add(Contract, where, "is no longer an MCP tool")
			}
			if o.CLI && !n.CLI {
				d.add(Contract, where, "is no longer a CLI command")
			}
			if o.CLI && n.CLI {
				for _, f := range []struct{ role, old, new string }{{"its CLI argument", o.CLIArg, n.CLIArg}, {"the words after --", o.CLIRest, n.CLIRest}, {"its secret from stdin", o.CLISecret, n.CLISecret}} {
					if f.old != f.new {
						d.add(Contract, where, "takes %s as %s now, was %s", fieldWord(f.new), f.role, cmp.Or(f.old, "none"))
					}
				}
			}
		}
	}
}

func fieldWord(name string) string {
	if name == "" {
		return "no field"
	}
	return "field " + name
}

func effectWord(e string) string {
	if e == "" {
		return "of unknown effect"
	}
	return strings.ToUpper(e)
}

func (d *diff) route(where string, o, n *HTTP) (kept bool) {
	switch {
	case o == nil:
		return false
	case n == nil:
		d.add(Contract, where, "REST route %s %s was removed", o.Method, o.Path)
		return false
	case routeKey(o) != routeKey(n):
		d.add(Contract, where, "REST route %s %s is %s %s now", o.Method, o.Path, n.Method, n.Path)
		return false
	}
	if o.Body != n.Body {
		d.add(Contract, where, "reads the %s now, was the %s", bodyWord(n.Body), bodyWord(o.Body))
	}
	if o.Anonymous && !n.Anonymous {
		d.add(Contract, where, "needs a signed-in caller now")
	}
	return true
}

func was(old string) string {
	if old == "" {
		return ""
	}
	return ", was " + old
}

func bodyWord(body string) string {
	if body == "*" {
		return "body"
	}
	return "query"
}

func (d *diff) headers(where string, o, n Message) {
	byName := map[string]Field{}
	for _, f := range n.Fields {
		byName[f.Name] = f
	}
	for _, of := range o.Fields {
		nf, ok := byName[of.Name]
		switch {
		case !ok || of.Header == nf.Header:
		case nf.Header == "":
			d.add(Contract, where, "field %s no longer comes from header %s", of.Name, of.Header)
		default:
			d.add(Contract, where, "field %s comes from header %s now%s", of.Name, nf.Header, was(of.Header))
		}
	}
}
