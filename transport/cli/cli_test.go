package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/typepb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/transport/internal/testpb"
)

type result struct {
	code        int
	out, errOut string
}

func run(m *Mux, stdin string, args ...string) result {
	var out, errOut bytes.Buffer
	code := m.Run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return result{code, out.String(), errOut.String()}
}

func echoing(got *proto.Message) func(context.Context, *testpb.Thing) (*testpb.Thing, error) {
	return func(_ context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		*got = in
		return in, nil
	}
}

func things(c Command, got *proto.Message) *Mux {
	m := NewMux("acme")
	if c.Service == "" {
		c.Service, c.Name = "things", "put"
	}
	Register(m, c, echoing(got))
	return m
}

func sameAs(t *testing.T, got proto.Message, want string) {
	t.Helper()
	w := &testpb.Thing{}
	if err := protojson.Unmarshal([]byte(want), w); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, w) {
		t.Fatalf("request = %v, want %v", got, w)
	}
}

func TestFlagsFillTheRequestFromItsDescriptor(t *testing.T) {
	var got proto.Message
	m := things(Command{}, &got)
	r := run(m, "", "things", "put", "--id", "t1", "--page=3", "--views", "9", "--active", "true", "--tags", "a", "--tags", "b",
		"--score", "1.5", "--user-agent", "ua", "--big", "7")
	if r.code != ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	sameAs(t, got, `{"id":"t1","page":3,"views":"9","active":true,"tags":["a","b"],"score":1.5,"userAgent":"ua","big":"7"}`)
	if !strings.Contains(r.out, "\n  \"id\": \"t1\"") {
		t.Fatalf("output is not indented JSON:\n%s", r.out)
	}
	if r := run(m, "", "things", "put", "--userAgent", "x", "--json"); r.code != ExitOK || r.out != "{\"userAgent\":\"x\"}\n" {
		t.Fatalf("--json = %d %q %s", r.code, r.out, r.errOut)
	}
}

func TestArgRestAndInputFillWhatFlagsCannot(t *testing.T) {
	var got proto.Message
	m := things(Command{Arg: "id", Rest: "tags"}, &got)
	if r := run(m, "", "things", "put", "t9", "--page", "2", "--", "x", "--y"); r.code != ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	sameAs(t, got, `{"id":"t9","page":2,"tags":["x","--y"]}`)
	if r := run(m, `{"child":{"name":"kid"}}`, "things", "put", "--input", "-", "--views", "4"); r.code != ExitOK {
		t.Fatalf("--input - exit %d: %s", r.code, r.errOut)
	}
	sameAs(t, got, `{"child":{"name":"kid"},"views":"4"}`)
	if r := run(m, "", "things", "put", "--input", `{"children":[{"name":"a"}]}`); r.code != ExitOK {
		t.Fatalf("--input exit %d: %s", r.code, r.errOut)
	}
	sameAs(t, got, `{"children":[{"name":"a"}]}`)
}

func TestWhatTheCommandLineCannotSayIsAUsageError(t *testing.T) {
	var got proto.Message
	m := things(Command{Arg: "id"}, &got)
	noRest := things(Command{}, &got)
	for name, c := range map[string]struct {
		m    *Mux
		args []string
		want string
	}{
		"unknown flag":   {m, []string{"--nope", "1"}, "no flag --nope"},
		"no value":       {m, []string{"--page"}, "--page needs a value"},
		"bad int":        {m, []string{"--page", "x"}, `"x" is not a 32-bit integer`},
		"twice":          {m, []string{"--page", "1", "--page", "2"}, "given twice"},
		"message flag":   {m, []string{"--child", "x"}, "pass it with --input"},
		"bytes flag":     {m, []string{"--blob", "x"}, "cannot be set from a flag"},
		"two words":      {m, []string{"a", "b"}, "unexpected words"},
		"word, no arg":   {noRest, []string{"a"}, "unexpected words"},
		"rest, no rest":  {noRest, []string{"--", "a"}, "no words after --"},
		"bad input":      {m, []string{"--input", "{"}, "--input"},
		"bad timeout":    {m, []string{"--timeout", "soon"}, "--timeout"},
		"json value":     {m, []string{"--json=1"}, "--json takes no value"},
		"bad positional": {things(Command{Arg: "page"}, &got), []string{"x"}, "page:"},
	} {
		got = nil
		r := run(c.m, "", append([]string{"things", "put"}, c.args...)...)
		if r.code != ExitUsage || !strings.Contains(r.errOut, c.want) || got != nil {
			t.Errorf("%s: exit %d %q, ran=%v", name, r.code, r.errOut, got != nil)
		}
	}
	if r := run(m, "", "things", "nope"); r.code != ExitUsage || !strings.Contains(r.errOut, `no command "things nope"`) {
		t.Errorf("unknown command: %d %q", r.code, r.errOut)
	}
}

func TestASecretIsReadFromStdinAndNeverFromTheCommandLine(t *testing.T) {
	var got proto.Message
	m := things(Command{Secret: "user_agent"}, &got)
	if r := run(m, "s3cret\nnext\n", "things", "put", "--user-agent", "-"); r.code != ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	sameAs(t, got, `{"userAgent":"s3cret"}`)
	got = nil
	if r := run(m, "", "things", "put", "--userAgent", "s3cret"); r.code != ExitUsage || !strings.Contains(r.errOut, "is a secret") || got != nil {
		t.Fatalf("a secret on the command line = %d %q", r.code, r.errOut)
	}
	if r := run(m, "", "things", "put", "--userAgent", "-"); r.code != ExitUsage || !strings.Contains(r.errOut, "nothing on stdin") {
		t.Fatalf("an empty stdin = %d %q", r.code, r.errOut)
	}
}

func TestADestructiveCommandRunsOnlyWithYes(t *testing.T) {
	var got proto.Message
	m := things(Command{Destructive: true}, &got)
	if r := run(m, "y\n", "things", "put", "--id", "a"); r.code != ExitDenied || got != nil {
		t.Fatalf("without --yes = %d, ran=%v", r.code, got != nil)
	}
	if r := run(m, "", "things", "put", "--id", "a", "--yes"); r.code != ExitOK || got == nil {
		t.Fatalf("with --yes = %d %s", r.code, r.errOut)
	}
	if r := run(m, "", "things", "put", "--yes=1"); r.code != ExitUsage {
		t.Fatalf("--yes=1 = %d", r.code)
	}
}

func TestTheExitCodeSaysHowTheCallEnded(t *testing.T) {
	for code, want := range map[connect.Code]int{
		connect.CodeNotFound:           ExitNotFound,
		connect.CodeUnauthenticated:    ExitDenied,
		connect.CodePermissionDenied:   ExitDenied,
		connect.CodeInvalidArgument:    ExitUsage,
		connect.CodeFailedPrecondition: ExitUsage,
		connect.CodeOutOfRange:         ExitUsage,
		connect.CodeUnavailable:        ExitUnavailable,
		connect.CodeResourceExhausted:  ExitOverloaded,
		connect.CodeDeadlineExceeded:   ExitTimeout,
		connect.CodeInternal:           ExitError,
	} {
		m := NewMux("acme")
		Register(m, Command{Service: "things", Name: "put"}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) {
			return nil, connect.NewError(code, errors.New("said no"))
		})
		if r := run(m, "", "things", "put"); r.code != want || !strings.Contains(r.errOut, "said no") {
			t.Errorf("%v = %d %q, want %d", code, r.code, r.errOut, want)
		}
	}
}

func TestATimeoutBoundsTheCall(t *testing.T) {
	wait := func(ctx context.Context, _ *testpb.Thing) (*testpb.Thing, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m := NewMux("acme")
	Register(m, Command{Service: "things", Name: "put", Timeout: 20 * time.Millisecond}, wait)
	Register(m, Command{Service: "things", Name: "slow"}, func(ctx context.Context, in *testpb.Thing) (*testpb.Thing, error) {
		if _, ok := ctx.Deadline(); ok {
			return nil, errors.New("a command with no timeout got a deadline")
		}
		return in, nil
	})
	if r := run(m, "", "things", "put"); r.code != ExitTimeout {
		t.Fatalf("the command's own timeout = %d %q", r.code, r.errOut)
	}
	Register(m, Command{Service: "things", Name: "wait"}, wait)
	if r := run(m, "", "things", "wait", "--timeout", "5ms"); r.code != ExitTimeout {
		t.Fatalf("--timeout = %d %q", r.code, r.errOut)
	}
	if r := run(m, "", "things", "wait", "--timeout", "-1s"); r.code != ExitUsage {
		t.Fatalf("a negative --timeout = %d", r.code)
	}
	if r := run(m, "", "things", "slow"); r.code != ExitOK {
		t.Fatalf("no timeout = %d %q", r.code, r.errOut)
	}
}

func TestAStreamPrintsOneLinePerMessage(t *testing.T) {
	m := NewMux("acme")
	RegisterStream(m, Command{Service: "things", Name: "watch"}, func(_ context.Context, in *testpb.Thing, send func(*testpb.Thing) error) error {
		for _, tag := range in.GetTags() {
			if err := send(&testpb.Thing{Id: tag}); err != nil {
				return err
			}
		}
		return connect.NewError(connect.CodeUnavailable, errors.New("feed ended"))
	})
	r := run(m, "", "things", "watch", "--tags", "a", "--tags", "b")
	if r.out != "{\"id\":\"a\"}\n{\"id\":\"b\"}\n" || r.code != ExitUnavailable {
		t.Fatalf("stream = %d %q %q", r.code, r.out, r.errOut)
	}
}

func TestEveryScalarKindParses(t *testing.T) {
	for _, c := range []struct {
		msg      proto.Message
		ok, bad  string
		want     string
		mismatch string
	}{
		{&wrapperspb.Int32Value{}, "-5", "3000000000", `-5`, "32-bit integer"},
		{&wrapperspb.Int64Value{}, "-9000000000", "1.5", `"-9000000000"`, "an integer"},
		{&wrapperspb.UInt32Value{}, "4000000000", "5000000000", `4000000000`, "unsigned 32-bit"},
		{&wrapperspb.UInt64Value{}, "18000000000000000000", "-1", `"18000000000000000000"`, "unsigned integer"},
		{&wrapperspb.FloatValue{}, "0.25", "1e300", `0.25`, "a 32-bit number"},
		{&wrapperspb.DoubleValue{}, "1e300", "x", `1e+300`, "a number"},
		{&wrapperspb.BoolValue{}, "true", "yes", `true`, "true or false"},
	} {
		name := fmt.Sprintf("%T", c.msg)
		m := NewMux("acme")
		var got string
		register(m, c.msg, &got)
		if r := run(m, "", "w", "set", "--value", c.ok); r.code != ExitOK || got != c.want {
			t.Errorf("%s %s = %d %q %s", name, c.ok, r.code, got, r.errOut)
		}
		if r := run(m, "", "w", "set", "--value="+c.bad); r.code != ExitUsage || !strings.Contains(r.errOut, c.mismatch) {
			t.Errorf("%s %s = %d %q", name, c.bad, r.code, r.errOut)
		}
	}
	m := NewMux("acme")
	var field *typepb.Field
	Register(m, Command{Service: "f", Name: "set"}, func(_ context.Context, in *typepb.Field) (*typepb.Field, error) {
		field = in
		return in, nil
	})
	if r := run(m, "", "f", "set", "--kind", "TYPE_STRING"); r.code != ExitOK || field.GetKind() != typepb.Field_TYPE_STRING {
		t.Fatalf("enum = %d %v %s", r.code, field, r.errOut)
	}
	if r := run(m, "", "f", "set", "--kind", "STRING"); r.code != ExitUsage || !strings.Contains(r.errOut, "one of TYPE_UNKNOWN, TYPE_DOUBLE") {
		t.Fatalf("bad enum = %d %q", r.code, r.errOut)
	}
}

func register(m *Mux, sample proto.Message, got *string) {
	keep := func(v proto.Message) {
		b, _ := protojson.Marshal(v)
		*got = string(b)
	}
	c := Command{Service: "w", Name: "set"}
	switch sample.(type) {
	case *wrapperspb.Int32Value:
		Register(m, c, func(_ context.Context, in *wrapperspb.Int32Value) (*wrapperspb.Int32Value, error) {
			keep(in)
			return in, nil
		})
	case *wrapperspb.Int64Value:
		Register(m, c, func(_ context.Context, in *wrapperspb.Int64Value) (*wrapperspb.Int64Value, error) {
			keep(in)
			return in, nil
		})
	case *wrapperspb.UInt32Value:
		Register(m, c, func(_ context.Context, in *wrapperspb.UInt32Value) (*wrapperspb.UInt32Value, error) {
			keep(in)
			return in, nil
		})
	case *wrapperspb.UInt64Value:
		Register(m, c, func(_ context.Context, in *wrapperspb.UInt64Value) (*wrapperspb.UInt64Value, error) {
			keep(in)
			return in, nil
		})
	case *wrapperspb.FloatValue:
		Register(m, c, func(_ context.Context, in *wrapperspb.FloatValue) (*wrapperspb.FloatValue, error) {
			keep(in)
			return in, nil
		})
	case *wrapperspb.DoubleValue:
		Register(m, c, func(_ context.Context, in *wrapperspb.DoubleValue) (*wrapperspb.DoubleValue, error) {
			keep(in)
			return in, nil
		})
	case *wrapperspb.BoolValue:
		Register(m, c, func(_ context.Context, in *wrapperspb.BoolValue) (*wrapperspb.BoolValue, error) {
			keep(in)
			return in, nil
		})
	}
}

func TestHelpListsCommandsAndEachCommandsFlags(t *testing.T) {
	m := NewMux("acme")
	Register(m, Command{Service: "things", Name: "put", Doc: "Stores a thing.\nMore detail.", Arg: "id", Destructive: true, Secret: "userAgent"}, echoing(new(proto.Message)))
	Register(m, Command{Service: "other", Name: "get", Doc: "Reads."}, echoing(new(proto.Message)))
	r := run(m, "")
	if r.code != ExitOK || !strings.Contains(r.out, "things put  Stores a thing.\n") || strings.Contains(r.out, "More detail") || !strings.Contains(r.out, "other get") {
		t.Fatalf("help = %d\n%s", r.code, r.out)
	}
	for _, args := range [][]string{{"help", "other"}, {"other"}} {
		if r := run(m, "", args...); r.code != ExitOK || strings.Contains(r.out, "things put") || !strings.Contains(r.out, "other get") {
			t.Fatalf("%v =\n%s", args, r.out)
		}
	}
	r = run(m, "", "things", "put", "--help")
	for _, want := range []string{"usage: acme things put <id> [flags]", "More detail.", "--tags", "string, repeatable", "--child", "message, via --input", "--userAgent", "secret, - reads stdin", "--yes"} {
		if r.code != ExitOK || !strings.Contains(r.out, want) {
			t.Errorf("command help lacks %q:\n%s", want, r.out)
		}
	}
	if r := run(m, "", "help", "things", "nope"); r.code != ExitUsage {
		t.Errorf("help for no command = %d", r.code)
	}
}

func TestARegistrationThatCannotWorkPanics(t *testing.T) {
	ok := func(context.Context, *testpb.Thing) (*testpb.Thing, error) { return nil, nil }
	for name, c := range map[string]Command{
		"no service":    {Name: "put"},
		"no name":       {Service: "things"},
		"no such arg":   {Service: "things", Name: "put", Arg: "nope"},
		"list arg":      {Service: "things", Name: "put", Arg: "tags"},
		"message arg":   {Service: "things", Name: "put", Arg: "child"},
		"bytes arg":     {Service: "things", Name: "put", Arg: "blob"},
		"scalar rest":   {Service: "things", Name: "put", Rest: "id"},
		"message rest":  {Service: "things", Name: "put", Rest: "children"},
		"number secret": {Service: "things", Name: "put", Secret: "page"},
		"list secret":   {Service: "things", Name: "put", Secret: "tags"},
		"secret arg":    {Service: "things", Name: "put", Arg: "id", Secret: "id"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			Register(NewMux("acme"), c, ok)
		}()
	}
	m := NewMux("acme")
	Register(m, Command{Service: "things", Name: "put"}, ok)
	defer func() {
		if recover() == nil {
			t.Error("a repeated command did not panic")
		}
	}()
	Register(m, Command{Service: "things", Name: "put"}, ok)
}

func TestInputAfterFlagsFillsOnlyWhatTheFlagsLeftUnset(t *testing.T) {
	var got proto.Message
	m := things(Command{Secret: "user_agent"}, &got)
	r := run(m, "s3cret\n", "things", "put", "--views", "4", "--user-agent", "-", "--active=false", "--tags", "a", "--input", `{"page":2,"views":"9","active":true,"tags":["b"]}`)
	if r.code != ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	sameAs(t, got, `{"views":"4","userAgent":"s3cret","page":2,"tags":["a"]}`)
}

func TestAnInlineInputCannotCarryTheSecret(t *testing.T) {
	var got proto.Message
	m := things(Command{Secret: "user_agent"}, &got)
	if r := run(m, "", "things", "put", "--input", `{"userAgent":"hunter2"}`); r.code != ExitUsage || !strings.Contains(r.errOut, "secret") || got != nil {
		t.Fatalf("an inline secret = %d %q, ran=%v", r.code, r.errOut, got != nil)
	}
	if r := run(m, `{"userAgent":"hunter2"}`, "things", "put", "--input", "-"); r.code != ExitOK {
		t.Fatalf("a secret in --input - = %d %q", r.code, r.errOut)
	}
	sameAs(t, got, `{"userAgent":"hunter2"}`)
}

func TestAFaultFromAnInProcessHandlerSetsTheExitCode(t *testing.T) {
	for code, want := range map[string]int{
		fault.CodeNotFound:    ExitNotFound,
		fault.CodeDenied:      ExitDenied,
		fault.CodeInvalid:     ExitUsage,
		fault.CodeUnavailable: ExitUnavailable,
		fault.CodeOverloaded:  ExitOverloaded,
		fault.CodeInternal:    ExitError,
	} {
		m := NewMux("acme")
		Register(m, Command{Service: "things", Name: "put"}, func(context.Context, *testpb.Thing) (*testpb.Thing, error) {
			return nil, fault.New(code, "said no")
		})
		if r := run(m, "", "things", "put"); r.code != want {
			t.Errorf("%s = %d %q, want %d", code, r.code, r.errOut, want)
		}
	}
}

func TestHelpIsOnlyAFlagBeforeDashDashAndNeverAFlagsValue(t *testing.T) {
	var got proto.Message
	m := things(Command{Rest: "tags"}, &got)
	if r := run(m, "", "things", "put", "--", "-h", "--help"); r.code != ExitOK || strings.Contains(r.out, "usage:") {
		t.Fatalf("words after -- = %d %s", r.code, r.out)
	}
	sameAs(t, got, `{"tags":["-h","--help"]}`)
	if r := run(m, "", "things", "put", "--id", "-h", "--active", "--user-agent", "--help"); r.code != ExitOK || strings.Contains(r.out, "usage:") {
		t.Fatalf("flag values = %d %s", r.code, r.out)
	}
	sameAs(t, got, `{"id":"-h","active":true,"userAgent":"--help"}`)
	for _, args := range [][]string{{"put", "--active", "-h"}, {"put", "--json", "-h"}, {"--help"}} {
		if r := run(m, "", append([]string{"things"}, args...)...); r.code != ExitOK || !strings.Contains(r.out, "things put") {
			t.Fatalf("%v = %d %s", args, r.code, r.out)
		}
	}
}

func TestABoolFlagStandsAloneOrTakesTrueOrFalse(t *testing.T) {
	var got proto.Message
	m := things(Command{Arg: "id"}, &got)
	if r := run(m, "", "things", "put", "--active", "--json"); r.code != ExitOK || r.out != "{\"active\":true}\n" {
		t.Fatalf("--active --json = %d %q %s", r.code, r.out, r.errOut)
	}
	if r := run(m, "", "things", "put", "t1", "--active"); r.code != ExitOK {
		t.Fatalf("a trailing --active = %d %s", r.code, r.errOut)
	}
	sameAs(t, got, `{"id":"t1","active":true}`)
	if r := run(m, "", "things", "put", "--active", "false", "t1"); r.code != ExitOK {
		t.Fatalf("--active false = %d %s", r.code, r.errOut)
	}
	sameAs(t, got, `{"id":"t1"}`)
}
