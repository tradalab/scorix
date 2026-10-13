package runner

import "fmt"

// The app declares this message itself (the parser has no imports), and env is a
// list of pairs because map<> is refused everywhere.
var execSpecFields = map[string]struct {
	typ      string
	repeated bool
}{
	"path":  {"string", false},
	"args":  {"string", true},
	"env":   {"", true}, // a message with string name and string value
	"unset": {"string", true},
	"dir":   {"string", false},
}

// Each refusal is a command that would build and then not do what @cli promised.
func checkCLI(pf protoFile, svcName string, rpc protoRPC) error {
	where := fmt.Sprintf("rpc %s.%s", svcName, rpc.Name)
	if rpc.IsEvent {
		return fmt.Errorf("%s is both @cli and @event/@broadcast; a command has a reply and an event has none, so drop one", where)
	}
	if rpc.Doc == "" {
		return fmt.Errorf("%s is @cli with no description; `<app> help` prints the comment above the rpc, so write one", where)
	}
	byName := map[string]protoMessage{}
	for _, m := range pf.Messages {
		byName[m.Name] = m
	}
	if rpc.CLIArg != "" {
		f, ok := fieldOf(byName[rpc.RequestType], rpc.CLIArg)
		switch {
		case !ok:
			return fmt.Errorf("%s has `@cli arg=%s`, but %s has no field %q", where, rpc.CLIArg, rpc.RequestType, rpc.CLIArg)
		case f.Repeated || !isProtoScalar(f.Type) || f.Type == "bytes":
			return fmt.Errorf("%s has `@cli arg=%s`; a positional argument is one word, so the field must be a single string, number or bool", where, rpc.CLIArg)
		}
	}
	if rpc.CLIRest != "" {
		f, ok := fieldOf(byName[rpc.RequestType], rpc.CLIRest)
		if !ok || !f.Repeated || f.Type != "string" {
			return fmt.Errorf("%s has `@cli rest=%s`; the words after -- go to a repeated string field of %s", where, rpc.CLIRest, rpc.RequestType)
		}
		if rpc.CLIRest == rpc.CLIArg {
			return fmt.Errorf("%s uses %q for both arg= and rest=", where, rpc.CLIArg)
		}
	}
	if rpc.CLISecret != "" {
		f, ok := fieldOf(byName[rpc.RequestType], rpc.CLISecret)
		if !ok || f.Repeated || f.Type != "string" {
			return fmt.Errorf("%s has `@cli secret=%s`; a secret is read as one line of text, so it must be a single string field of %s", where, rpc.CLISecret, rpc.RequestType)
		}
		if rpc.CLISecret == rpc.CLIArg {
			return fmt.Errorf("%s uses %q for both arg= and secret=; a positional argument lands in shell history", where, rpc.CLIArg)
		}
	}
	if rpc.CLIExec {
		if rpc.Arity != "unary" {
			return fmt.Errorf("%s is `@cli exec` with the %s arity; the process becomes the program once, so only a unary rpc can exec", where, rpc.Arity)
		}
		if err := checkExecSpec(byName, rpc.ResponseType); err != nil {
			return fmt.Errorf("%s is `@cli exec`, but its reply %s %v", where, rpc.ResponseType, err)
		}
	}
	return nil
}

// The modifiers name proto fields, the request uses JSON names: a camelCase
// field would otherwise match nothing.
func cliJSONNames(pf protoFile, rpc *protoRPC) {
	var msg protoMessage
	for _, m := range pf.Messages {
		if m.Name == rpc.RequestType {
			msg = m
		}
	}
	for _, name := range []*string{&rpc.CLIArg, &rpc.CLIRest, &rpc.CLISecret} {
		if f, ok := fieldOf(msg, *name); ok && *name != "" {
			*name = f.JSONName
		}
	}
}

func checkExecSpec(byName map[string]protoMessage, name string) error {
	msg, ok := byName[name]
	if !ok {
		return fmt.Errorf("is not declared")
	}
	seen := map[string]bool{}
	for _, f := range msg.Fields {
		want, known := execSpecFields[f.Name]
		if !known {
			return fmt.Errorf("has field %q, which the CLI would drop; an exec reply is exactly path, args, env, unset, dir", f.Name)
		}
		if f.Repeated != want.repeated {
			return fmt.Errorf("field %q must be %s", f.Name, repeatedWord(want.repeated))
		}
		if f.Name == "env" {
			if err := checkEnvVar(byName, f.Type); err != nil {
				return err
			}
		} else if f.Type != want.typ {
			return fmt.Errorf("field %q must be %s, not %s", f.Name, want.typ, f.Type)
		}
		seen[f.Name] = true
	}
	if !seen["path"] {
		return fmt.Errorf("has no `path`, so there is nothing to run")
	}
	return nil
}

func checkEnvVar(byName map[string]protoMessage, name string) error {
	msg, ok := byName[name]
	if !ok {
		return fmt.Errorf("field \"env\" must be a repeated message with string name and string value; %s is not one", name)
	}
	n, okN := fieldOf(msg, "name")
	v, okV := fieldOf(msg, "value")
	if len(msg.Fields) != 2 || !okN || !okV || n.Type != "string" || v.Type != "string" || n.Repeated || v.Repeated {
		return fmt.Errorf("field \"env\" must be a repeated message with exactly string name and string value; %s is not", name)
	}
	return nil
}

func fieldOf(msg protoMessage, name string) (protoField, bool) {
	for _, f := range msg.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return protoField{}, false
}

func isProtoScalar(t string) bool {
	switch t {
	case "double", "float", "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64",
		"uint32", "fixed32", "uint64", "fixed64", "bool", "string", "bytes":
		return true
	}
	return false
}

func repeatedWord(r bool) string {
	if r {
		return "repeated"
	}
	return "a single value, not repeated"
}
