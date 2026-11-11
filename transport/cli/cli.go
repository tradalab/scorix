// Package cli runs a service's rpcs from a terminal: each command's flags come from its request
// descriptor, the reply prints as JSON, and the exit code says how the call ended.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	scorixconnect "github.com/tradalab/scorix/transport/connect"
)

const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitDenied      = 3
	ExitNotFound    = 4
	ExitUnavailable = 5
	ExitOverloaded  = 6
	ExitTimeout     = 7
)

// Command is one rpc on the command line, run as `<Service> <Name>`. Arg, Rest and Secret name
// request fields: the first positional word, the words after --, and a line read from stdin.
type Command struct {
	Service     string
	Name        string
	Doc         string
	Procedure   string
	Arg         string
	Rest        string
	Secret      string
	Destructive bool
	Timeout     time.Duration
}

type entry struct {
	Command
	in      func() proto.Message
	run     func(ctx context.Context, in proto.Message, print func(proto.Message) error) error
	arg     protoreflect.FieldDescriptor
	rest    protoreflect.FieldDescriptor
	secret  protoreflect.FieldDescriptor
	streams bool
}

type Mux struct {
	name string
	cmds []entry
}

// NewMux names the program in help and usage lines.
func NewMux(name string) *Mux { return &Mux{name: name} }

func message[T any]() proto.Message {
	m, ok := any(new(T)).(proto.Message)
	if !ok {
		panic(fmt.Sprintf("transport/cli: %T is not a proto message", new(T)))
	}
	return m
}

func (m *Mux) add(c Command, in proto.Message, streams bool, run func(context.Context, proto.Message, func(proto.Message) error) error) {
	if c.Service == "" || c.Name == "" {
		panic("transport/cli: " + c.Procedure + " needs a Service and a Name")
	}
	if _, dup := m.find(c.Service, c.Name); dup {
		panic("transport/cli: " + c.Service + " " + c.Name + " registered twice")
	}
	fields := in.ProtoReflect().Descriptor().Fields()
	e := entry{Command: c, run: run, streams: streams, in: func() proto.Message { return in.ProtoReflect().New().Interface() }}
	pick := func(role, name string, ok func(protoreflect.FieldDescriptor) bool) protoreflect.FieldDescriptor {
		if name == "" {
			return nil
		}
		f := fields.ByName(protoreflect.Name(name))
		if f == nil {
			f = fields.ByJSONName(name)
		}
		if f == nil || !ok(f) {
			panic(fmt.Sprintf("transport/cli: %s %s: %s %q is not a fitting field of %s", c.Service, c.Name, role, name, in.ProtoReflect().Descriptor().FullName()))
		}
		return f
	}
	single := func(f protoreflect.FieldDescriptor) bool {
		return !f.IsList() && !f.IsMap() && f.Kind() != protoreflect.MessageKind && f.Kind() != protoreflect.GroupKind && f.Kind() != protoreflect.BytesKind
	}
	strs := func(f protoreflect.FieldDescriptor) bool { return f.IsList() && f.Kind() == protoreflect.StringKind }
	str := func(f protoreflect.FieldDescriptor) bool { return !f.IsList() && f.Kind() == protoreflect.StringKind }
	e.arg = pick("arg", c.Arg, single)
	e.rest = pick("rest", c.Rest, strs)
	e.secret = pick("secret", c.Secret, str)
	if e.secret != nil && (e.secret == e.arg || e.secret == e.rest) {
		panic(fmt.Sprintf("transport/cli: %s %s: the secret %s cannot also be positional; it would land in shell history", c.Service, c.Name, c.Secret))
	}
	m.cmds = append(m.cmds, e)
}

// Register adds a unary rpc. It panics when a name is empty or repeats, or Arg, Rest or Secret names no
// fitting field: a single scalar, a repeated string, and a single string.
func Register[Req, Res any](m *Mux, c Command, fn func(context.Context, *Req) (*Res, error)) {
	m.add(c, message[Req](), false, func(ctx context.Context, in proto.Message, print func(proto.Message) error) error {
		out, err := fn(ctx, any(in).(*Req))
		if err != nil {
			return err
		}
		return print(any(out).(proto.Message))
	})
}

// RegisterStream adds a server-stream rpc; each message prints as it arrives, one JSON per line.
func RegisterStream[Req, Res any](m *Mux, c Command, fn func(context.Context, *Req, func(*Res) error) error) {
	m.add(c, message[Req](), true, func(ctx context.Context, in proto.Message, print func(proto.Message) error) error {
		return fn(ctx, any(in).(*Req), func(r *Res) error { return print(any(r).(proto.Message)) })
	})
}

func (m *Mux) find(service, name string) (entry, bool) {
	for _, e := range m.cmds {
		if e.Service == service && e.Name == name {
			return e, true
		}
	}
	return entry{}, false
}

// Run runs the command args name and returns its exit code. Flags are --<field> value for each
// scalar or repeated scalar request field, --input with the request as JSON (- reads stdin) for
// the fields the flags leave unset, --json for one-line output, --yes, and --timeout.
func (m *Mux) Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] == "help" {
		return m.help(stdout, stderr, args)
	}
	e, ok := m.find(args[0], args[1])
	if ok && e.asksHelp(args[2:]) || !ok && (slices.Contains(args, "--help") || slices.Contains(args, "-h")) {
		return m.help(stdout, stderr, args)
	}
	if !ok {
		fmt.Fprintf(stderr, "error: no command %q; run `%s help`\n", args[0]+" "+args[1], m.name)
		return ExitUsage
	}
	in, opt, err := e.parse(args[2:], bufio.NewReader(stdin))
	if err != nil {
		fmt.Fprintf(stderr, "error: %s %s: %v\n", e.Service, e.Name, err)
		return ExitUsage
	}
	if e.Destructive && !opt.yes {
		fmt.Fprintf(stderr, "error: %s %s changes or deletes data; pass --yes to run it\n", e.Service, e.Name)
		return ExitDenied
	}
	timeout := e.Timeout
	if opt.timeout > 0 {
		timeout = opt.timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	pretty := !opt.json && !e.streams
	err = e.run(ctx, in, func(out proto.Message) error {
		b, err := protojson.Marshal(out)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if pretty {
			err = json.Indent(&buf, b, "", "  ")
		} else {
			err = json.Compact(&buf, b)
		}
		if err != nil {
			return err
		}
		buf.WriteByte('\n')
		_, err = stdout.Write(buf.Bytes())
		return err
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitCode(err)
	}
	return ExitOK
}

func exitCode(err error) int {
	if errors.Is(err, context.DeadlineExceeded) {
		return ExitTimeout
	}
	switch scorixconnect.Code(err) {
	case connect.CodeNotFound:
		return ExitNotFound
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return ExitDenied
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeOutOfRange:
		return ExitUsage
	case connect.CodeUnavailable:
		return ExitUnavailable
	case connect.CodeResourceExhausted:
		return ExitOverloaded
	case connect.CodeDeadlineExceeded:
		return ExitTimeout
	}
	return ExitError
}

type options struct {
	json, yes bool
	timeout   time.Duration
}

func (e entry) parse(args []string, stdin *bufio.Reader) (proto.Message, options, error) {
	var opt options
	in := e.in()
	msg := in.ProtoReflect()
	var positional []string
	set := map[protoreflect.Name]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if e.rest == nil {
				return nil, opt, errors.New("takes no words after --")
			}
			list := msg.Mutable(e.rest).List()
			for _, w := range args[i+1:] {
				list.Append(protoreflect.ValueOfString(w))
			}
			break
		}
		if !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
			continue
		}
		name, value, inline := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch name {
		case "json", "yes":
			if inline {
				return nil, opt, fmt.Errorf("--%s takes no value", name)
			}
			if name == "json" {
				opt.json = true
			} else {
				opt.yes = true
			}
			continue
		}
		switch {
		case inline:
		case !e.takesValue(name, args[i+1:]):
			value = "true"
		case i+1 >= len(args):
			return nil, opt, fmt.Errorf("--%s needs a value", name)
		default:
			i++
			value = args[i]
		}
		switch name {
		case "timeout":
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return nil, opt, fmt.Errorf("--timeout %q is not a positive duration such as 30s", value)
			}
			opt.timeout = d
			continue
		case "input":
			piped := value == "-"
			if piped {
				b, err := io.ReadAll(stdin)
				if err != nil {
					return nil, opt, err
				}
				value = string(b)
			}
			given := e.in()
			if err := (protojson.UnmarshalOptions{}).Unmarshal([]byte(value), given); err != nil {
				return nil, opt, fmt.Errorf("--input: %v", err)
			}
			if e.secret != nil && !piped && given.ProtoReflect().Has(e.secret) {
				return nil, opt, fmt.Errorf("--input sets the secret %s; pass --input - and send the JSON on stdin, so it stays out of shell history", e.secret.JSONName())
			}
			given.ProtoReflect().Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
				if !set[f.Name()] && !msg.Has(f) {
					msg.Set(f, v)
				}
				return true
			})
			continue
		}
		f := e.field(name)
		if f == nil {
			return nil, opt, fmt.Errorf("no flag --%s; run with --help", name)
		}
		if f == e.secret {
			if value != "-" {
				return nil, opt, fmt.Errorf("--%s is a secret; pass - and type it on stdin, so it stays out of shell history", name)
			}
			line, err := stdin.ReadString('\n')
			if err != nil && line == "" {
				return nil, opt, fmt.Errorf("--%s: nothing on stdin", name)
			}
			value = strings.TrimRight(line, "\r\n")
		}
		if err := assign(msg, f, value, set); err != nil {
			return nil, opt, fmt.Errorf("--%s: %v", name, err)
		}
	}
	if len(positional) > 0 {
		if e.arg == nil || len(positional) > 1 {
			return nil, opt, fmt.Errorf("unexpected words %q", positional)
		}
		if err := assign(msg, e.arg, positional[0], set); err != nil {
			return nil, opt, fmt.Errorf("%s: %v", e.arg.JSONName(), err)
		}
	}
	return in, opt, nil
}

func (e entry) field(name string) protoreflect.FieldDescriptor {
	fields := e.in().ProtoReflect().Descriptor().Fields()
	if f := fields.ByJSONName(name); f != nil {
		return f
	}
	return fields.ByName(protoreflect.Name(strings.ReplaceAll(name, "-", "_")))
}

func (e entry) takesValue(name string, rest []string) bool {
	if f := e.field(name); f != nil && f.Kind() == protoreflect.BoolKind {
		return len(rest) > 0 && (rest[0] == "true" || rest[0] == "false")
	}
	return true
}

func (e entry) asksHelp(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return false
		case a == "--help" || a == "-h":
			return true
		case strings.HasPrefix(a, "--") && !strings.Contains(a, "=") && a != "--json" && a != "--yes" && e.takesValue(a[2:], args[i+1:]):
			i++
		}
	}
	return false
}

func assign(msg protoreflect.Message, f protoreflect.FieldDescriptor, raw string, set map[protoreflect.Name]bool) error {
	if f.IsMap() || f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
		return errors.New("is a message; pass it with --input")
	}
	v, err := scalar(f, raw)
	if err != nil {
		return err
	}
	if f.IsList() {
		msg.Mutable(f).List().Append(v)
		return nil
	}
	if set[f.Name()] {
		return errors.New("given twice")
	}
	set[f.Name()] = true
	msg.Set(f, v)
	return nil
}

func scalar(f protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	bad := func(want string) (protoreflect.Value, error) {
		return protoreflect.Value{}, fmt.Errorf("%q is not %s", raw, want)
	}
	switch f.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return bad("true or false")
		}
		return protoreflect.ValueOfBool(b), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return bad("a 32-bit integer")
		}
		return protoreflect.ValueOfInt32(int32(n)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return bad("an integer")
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return bad("an unsigned 32-bit integer")
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return bad("an unsigned integer")
		}
		return protoreflect.ValueOfUint64(n), nil
	case protoreflect.FloatKind:
		n, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return bad("a 32-bit number")
		}
		return protoreflect.ValueOfFloat32(float32(n)), nil
	case protoreflect.DoubleKind:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return bad("a number")
		}
		return protoreflect.ValueOfFloat64(n), nil
	case protoreflect.EnumKind:
		v := f.Enum().Values().ByName(protoreflect.Name(raw))
		if v == nil {
			var names []string
			for i := range f.Enum().Values().Len() {
				names = append(names, string(f.Enum().Values().Get(i).Name()))
			}
			return bad("one of " + strings.Join(names, ", "))
		}
		return protoreflect.ValueOfEnum(v.Number()), nil
	}
	return protoreflect.Value{}, fmt.Errorf("a %s field cannot be set from a flag; use --input", f.Kind())
}

func (m *Mux) help(stdout, stderr io.Writer, args []string) int {
	words := slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "help" || a == "--help" || a == "-h" })
	if len(words) >= 2 {
		e, ok := m.find(words[0], words[1])
		if !ok {
			fmt.Fprintf(stderr, "error: no command %q\n", words[0]+" "+words[1])
			return ExitUsage
		}
		e.usage(stdout, m.name)
		return ExitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "usage: %s <service> <command> [flags]\n\n", m.name)
	for _, e := range m.cmds {
		if len(words) == 1 && e.Service != words[0] {
			continue
		}
		fmt.Fprintf(tw, "  %s %s\t%s\n", e.Service, e.Name, firstLine(e.Doc))
	}
	tw.Flush()
	return ExitOK
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func (e entry) usage(w io.Writer, program string) {
	head := program + " " + e.Service + " " + e.Name
	if e.arg != nil {
		head += " <" + e.arg.JSONName() + ">"
	}
	if e.rest != nil {
		head += " [-- " + e.rest.JSONName() + "...]"
	}
	fmt.Fprintf(w, "usage: %s [flags]\n", head)
	if e.Doc != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(e.Doc))
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nflags:")
	fields := e.in().ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		kind := f.Kind().String()
		switch {
		case f.IsMap() || f.Kind() == protoreflect.MessageKind:
			kind = "message, via --input"
		case f.IsList():
			kind += ", repeatable"
		case f == e.secret:
			kind = "secret, - reads stdin"
		}
		fmt.Fprintf(tw, "  --%s\t%s\n", f.JSONName(), kind)
	}
	fmt.Fprintln(tw, "  --input\tthe whole request as JSON, - reads stdin")
	fmt.Fprintln(tw, "  --json\tprint the reply on one line")
	if e.Destructive {
		fmt.Fprintln(tw, "  --yes\trun it; this command changes or deletes data")
	}
	fmt.Fprintln(tw, "  --timeout\thow long the call may run, such as 30s")
	tw.Flush()
}
