package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tradalab/scorix/fault"
	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/logger"
)

// CLICommand is registered by generated code for each @cli rpc.
type CLICommand struct {
	Command string // wire name, e.g. account:run
	Service string // first word on the command line, e.g. account
	Doc     string
	// Request JSON schema; the flags are derived from it at run time.
	Schema      json.RawMessage
	Arg         string // field given positionally
	Rest        string // repeated string field taking the words the command does not read itself
	Secret      string // string field read from stdin when its flag is given as -
	Destructive bool
	Headless    bool
	Exec        bool
	Stream      bool // server-stream: each chunk prints as it arrives
	Timeout     time.Duration
}

type cliCallKey struct{}

// CLICall reports a call from the command line, where a destructive command was
// already confirmed: a handler must not ask again on a screen nobody watches.
func CLICall(ctx context.Context) bool {
	v, _ := ctx.Value(cliCallKey{}).(bool)
	return v
}

func (a *App) CLICommand(c CLICommand) {
	a.mu.Lock()
	a.cliCmds = append(a.cliCmds, c)
	a.mu.Unlock()
	// An exec reply can carry a key unsealed for the program, and the page and
	// web mode reach every IPC command: only the command line may get one.
	if c.Exec {
		a.reg.Guard(c.Command, func(fn ipc.CmdFunc) ipc.CmdFunc {
			return func(ctx context.Context, data json.RawMessage, s ipc.Stream) (any, error) {
				if !CLICall(ctx) {
					return nil, fault.New(fault.CodeDenied, c.Command+" runs a program and answers the command line only")
				}
				return fn(ctx, data, s)
			}
		})
	}
}

// Exit codes are a contract with scripts, so they are fixed here and nowhere else.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitDenied      = 3
	ExitNotFound    = 4
	ExitUnavailable = 5
	ExitOverloaded  = 6
	ExitTimeout     = 7
	ExitInterrupted = 130
)

var cliKeywords = map[string]bool{"call": true, "help": true, "version": true, "--version": true, "--help": true, "cli": true}

// IsCLI decides on the first word only, against a closed list: the same argv
// also carries files and URLs the OS asks the app to open.
func IsCLI(args, words []string) bool {
	if len(args) == 0 {
		return false
	}
	if cliKeywords[args[0]] {
		return true
	}
	for _, w := range words {
		if args[0] == w {
			return true
		}
	}
	return false
}

// RunCLI runs one command and returns the exit code. setup is what main does
// between New and Run; no window is created.
func RunCLI(manifest []byte, args []string, setup func(*App) error) int {
	// Before New, since stdout is the command's output. Errors only: the start-up
	// audit warns about the window's allowlist, noise to a command's reader.
	logger.New(logger.Config{Level: "error", Format: "console", Output: "stderr"})
	a, err := New(Options{Manifest: manifest})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return ExitError
	}
	if !a.cfg.CLI.Enabled {
		fmt.Fprintln(os.Stderr, "error: this build has no command line (cli.enabled is off in scorix.yaml)")
		return ExitDenied
	}
	if err := setup(a); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return ExitError
	}
	return a.runCLI(args, cliIO{in: os.Stdin, out: os.Stdout, err: os.Stderr, tty: isTerminal(os.Stdin)})
}

type cliIO struct {
	in       io.Reader
	out, err io.Writer
	tty      bool
}

type cliOpts struct {
	json, yes, launch, help bool
	input                   string
	timeout                 time.Duration
}

func (a *App) runCLI(args []string, io cliIO) int {
	opts, words, rest, err := splitCLIArgs(args, a.cliByWords)
	if err != nil {
		fmt.Fprintln(io.err, "error:", err)
		return ExitUsage
	}
	if len(words) == 0 || opts.help {
		return a.cliHelp(io.out, words)
	}
	switch words[0] {
	case "help":
		return a.cliHelp(io.out, words[1:])
	case "--version", "version":
		fmt.Fprintf(io.out, "%s %s\n", a.cfg.App.Name, a.cfg.App.Version)
		return ExitOK
	case "cli":
		verb := ""
		if len(words) > 1 {
			verb = words[1]
		}
		return a.runCLIInstall(verb, io, opts.json)
	}

	var cmd CLICommand
	var req map[string]any
	if words[0] == "call" {
		if len(words) < 2 {
			fmt.Fprintln(io.err, "error: usage: call <service:method> [<json>|-]")
			return ExitUsage
		}
		c, code := a.cliFind(io.err, words[1])
		if code != ExitOK {
			return code
		}
		cmd = c
		if req, err = callBody(words[2:], io.in); err != nil {
			fmt.Fprintln(io.err, "error:", err)
			return ExitUsage
		}
		// A secret in argv stays in history and ps. Any case of the key counts:
		// json.Unmarshal matches field names case-insensitively.
		if c.Secret != "" && (len(words) < 3 || words[2] != "-") {
			for k, v := range req {
				if s, _ := v.(string); strings.EqualFold(k, c.Secret) && s != "" {
					fmt.Fprintf(io.err, "error: %s carries a secret in %q; pass the JSON on stdin with `-`\n", c.Command, c.Secret)
					return ExitUsage
				}
			}
		}
	} else {
		if len(words) < 2 {
			return a.cliHelp(io.out, words)
		}
		c, ok := a.cliByWords(words[0], words[1])
		if !ok {
			fmt.Fprintf(io.err, "error: no command %q; run `help %s`\n", words[0]+" "+words[1], words[0])
			return ExitUsage
		}
		cmd = c
		if req, err = buildRequest(cmd, opts, words[2:], rest, io); err != nil {
			fmt.Fprintln(io.err, "error:", err)
			return ExitUsage
		}
	}

	if cmd.Destructive && !opts.yes {
		if !io.tty {
			fmt.Fprintf(io.err, "error: %s changes or deletes data; pass --yes to run it without a terminal\n", cmd.Command)
			return ExitDenied
		}
		fmt.Fprintf(io.err, "Run %s? [y/N] ", cmd.Command)
		line, _ := bufio.NewReader(io.in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return ExitDenied
		}
	}
	body, _ := json.Marshal(req)
	// A running app goes first even for a headless command: one writer, not two.
	chunk := chunkPrinter(io.out, opts.json)
	res, code, attached := a.cliAttach(cmd, opts, body, chunk, io.err)
	if !attached && opts.launch {
		if err := a.cliLaunchAndWait(); err != nil {
			fmt.Fprintln(io.err, "error:", err)
			return ExitUnavailable
		}
		res, code, attached = a.cliAttach(cmd, opts, body, chunk, io.err)
	}
	if !attached {
		if !cmd.Headless {
			fmt.Fprintf(io.err, "error: %s needs %s to be open; start it, or run this again with --launch\n", cmd.Command, a.cfg.App.Name)
			return ExitUnavailable
		}
		res, code = a.cliInvoke(cmd, opts, body, chunk, io.err)
	}
	if cmd.Stream {
		return code
	}
	if code != ExitOK {
		return code
	}
	if cmd.Exec {
		var spec ExecSpec
		if err := json.Unmarshal(res, &spec); err != nil || spec.Path == "" {
			fmt.Fprintf(io.err, "error: %s returned no program to run\n", cmd.Command)
			return ExitError
		}
		return runExec(spec, io.err)
	}
	if opts.json {
		if len(res) == 0 {
			res = []byte("null")
		}
		fmt.Fprintln(io.out, string(res))
		return ExitOK
	}
	renderHuman(io.out, res)
	return ExitOK
}

// The modules stop before an exec: the program taking over must not inherit
// open databases.
func (a *App) cliInvoke(cmd CLICommand, opts cliOpts, body []byte, chunk func(json.RawMessage), errw io.Writer) (json.RawMessage, int) {
	if err := a.startModules(); err != nil {
		fmt.Fprintln(errw, "error:", err)
		return nil, ExitError
	}
	defer a.stopModules()
	ctx, stop := signal.NotifyContext(context.WithValue(context.Background(), cliCallKey{}, true), os.Interrupt)
	defer stop()
	timeout := opts.timeout
	if timeout == 0 {
		timeout = cmd.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	res, err := a.cliCall(ctx, cmd, body, chunk)
	if err != nil {
		fmt.Fprintln(errw, "error:", err)
		return nil, exitCodeOf(ctx, err)
	}
	return res, ExitOK
}

// Shared by the headless path and the attach socket, so both run a command alike.
func (a *App) cliCall(ctx context.Context, cmd CLICommand, body []byte, chunk func(json.RawMessage)) (json.RawMessage, error) {
	if !cmd.Stream {
		return a.reg.Invoke(ctx, cmd.Command, body)
	}
	ok, err := a.reg.InvokeStream(ctx, cmd.Command, body, func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		chunk(b)
		return nil
	})
	if !ok {
		return nil, fmt.Errorf("%s is not a stream", cmd.Command)
	}
	return nil, err
}

// A chunk with one field prints as that field, so a text delta reads as text.
func chunkPrinter(w io.Writer, asJSON bool) func(json.RawMessage) {
	return func(b json.RawMessage) {
		if asJSON {
			fmt.Fprintln(w, string(b))
			return
		}
		if keys, obj := orderedObject(b); len(keys) == 1 {
			fmt.Fprintln(w, scalarText(obj[keys[0]]))
			return
		}
		fmt.Fprintln(w, string(b))
	}
}

func exitCodeOf(ctx context.Context, err error) int {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ExitInterrupted
	}
	return exitCodeFor(codeOf(ctx, err))
}

func (a *App) cliCommands() []CLICommand {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]CLICommand(nil), a.cliCmds...)
}

func (a *App) cliByWords(service, method string) (CLICommand, bool) {
	for _, c := range a.cliCommands() {
		if c.Service == service && methodWord(c.Command) == method {
			return c, true
		}
	}
	return CLICommand{}, false
}

// `call` must not reach past @cli: a known command without it is refused.
func (a *App) cliFind(errw io.Writer, command string) (CLICommand, int) {
	for _, c := range a.cliCommands() {
		if c.Command == command {
			return c, ExitOK
		}
	}
	if a.reg.Has(command) {
		fmt.Fprintf(errw, "error: %s is not offered on the command line\n", command)
		return CLICommand{}, ExitDenied
	}
	fmt.Fprintf(errw, "error: no command %s\n", command)
	return CLICommand{}, ExitUsage
}

func methodWord(command string) string {
	if i := strings.IndexByte(command, ':'); i >= 0 {
		return command[i+1:]
	}
	return command
}

// Everything after a bare -- passes through untouched, for the program to read.
func splitCLIArgs(args []string, byWords func(service, method string) (CLICommand, bool)) (cliOpts, []string, []string, error) {
	var o cliOpts
	var words, rest []string
	cut := false
	for i := 0; i < len(args); i++ {
		if !cut && len(words) == 2 {
			if cmd, ok := byWords(words[0], words[1]); ok && cmd.Rest != "" {
				var own []string
				own, rest = cutRest(cmd, args[i:])
				args = append(args[:i:i], own...)
				cut = true
				if i >= len(args) {
					break
				}
			}
		}
		arg := args[i]
		switch {
		case arg == "--":
			rest = append([]string{}, args[i+1:]...)
			return o, words, rest, nil
		case arg == "--json":
			o.json = true
		case arg == "--yes" || arg == "-y":
			o.yes = true
		case arg == "--launch":
			o.launch = true
		// After the command too: `<app> claude run --help` would otherwise be parsed
		// as a flag of that command and refused.
		case arg == "--help" || arg == "-h":
			o.help = true
		case arg == "--input" || arg == "--timeout":
			if i+1 >= len(args) {
				return o, nil, nil, fmt.Errorf("%s needs a value", arg)
			}
			i++
			if arg == "--input" {
				o.input = args[i]
				continue
			}
			d, err := time.ParseDuration(args[i])
			if err != nil || d <= 0 {
				return o, nil, nil, fmt.Errorf("--timeout takes a duration such as 30s")
			}
			o.timeout = d
		default:
			words = append(words, arg)
		}
	}
	return o, words, rest, nil
}

// rest= words belong to another program and are typed as for it: `run work -r`
// needs no --. The first word this command cannot read starts them.
func cutRest(cmd CLICommand, args []string) (own, rest []string) {
	flags := flagsOf(cmd.Schema)
	positional := cmd.Arg == ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return args[:i:i], args[i+1:]
		case cliGlobalFlag[arg]:
			if arg == "--input" || arg == "--timeout" {
				i++
			}
			continue
		case !strings.HasPrefix(arg, "-"):
			if positional {
				return args[:i:i], args[i:]
			}
			positional = true
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		spec, ok := flags[name]
		if !strings.HasPrefix(arg, "--") || !ok || spec.field == cmd.Rest {
			return args[:i:i], args[i:]
		}
		if spec.field == cmd.Arg {
			positional = true
		}
		if spec.typ != "boolean" && !hasValue {
			i++
		}
	}
	return args, nil
}

var cliGlobalFlag = map[string]bool{
	"--json": true, "--yes": true, "-y": true, "--launch": true, "--help": true, "-h": true, "--input": true, "--timeout": true,
}

func callBody(args []string, in io.Reader) (map[string]any, error) {
	if len(args) == 0 {
		return map[string]any{}, nil
	}
	if len(args) > 1 {
		return nil, fmt.Errorf("call takes one JSON argument; quote it")
	}
	raw := []byte(args[0])
	if args[0] == "-" {
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the request is not a JSON object: %w", err)
	}
	return m, nil
}

type flagSpec struct {
	field string
	typ   string // string | integer | number | boolean
	array bool
}

func flagsOf(schema json.RawMessage) map[string]flagSpec {
	var s struct {
		Properties map[string]struct {
			Type  string `json:"type"`
			Items *struct {
				Type string `json:"type"`
			} `json:"items"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	out := map[string]flagSpec{}
	for name, p := range s.Properties {
		spec := flagSpec{field: name, typ: p.Type}
		if p.Type == "array" {
			if p.Items == nil || p.Items.Type == "object" || p.Items.Type == "array" {
				continue
			}
			spec = flagSpec{field: name, typ: p.Items.Type, array: true}
		}
		// A nested message has no one-word spelling; --input sets it.
		if spec.typ == "object" {
			continue
		}
		out[strings.ReplaceAll(name, "_", "-")] = spec
	}
	return out
}

func buildRequest(cmd CLICommand, o cliOpts, args, rest []string, cio cliIO) (map[string]any, error) {
	in := cio.in
	req := map[string]any{}
	if o.input != "" {
		var raw []byte
		var err error
		if o.input == "-" {
			raw, err = io.ReadAll(in)
		} else {
			raw, err = os.ReadFile(o.input)
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("--input is not a JSON object: %w", err)
		}
	}
	flags := flagsOf(cmd.Schema)
	positional := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			if cmd.Arg == "" || positional > 0 {
				return nil, fmt.Errorf("unexpected %q", arg)
			}
			// Typed like its flag: "7" as a string fails an int32 unmarshal.
			typ := "string"
			for _, f := range flags {
				if f.field == cmd.Arg && !f.array {
					typ = f.typ
				}
			}
			v, err := flagValue(typ, arg)
			if err != nil {
				return nil, fmt.Errorf("<%s>: %w", cmd.Arg, err)
			}
			req[cmd.Arg] = v
			positional++
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		spec, ok := flags[name]
		if !ok {
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
		if spec.typ == "boolean" && !hasValue {
			value, hasValue = "true", true
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--%s needs a value", name)
			}
			i++
			value = args[i]
		}
		// A secret typed here stays in shell history and ps; stdin only.
		if spec.field == cmd.Secret && cmd.Secret != "" {
			if value != "-" {
				return nil, fmt.Errorf("--%s takes - and reads the value from stdin; a value typed here stays in your shell history", name)
			}
			secret, err := readSecret(cio, name)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", name, err)
			}
			req[spec.field] = secret
			continue
		}
		v, err := flagValue(spec.typ, value)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", name, err)
		}
		if spec.array {
			list, _ := req[spec.field].([]any)
			req[spec.field] = append(list, v)
		} else {
			req[spec.field] = v
		}
	}
	if len(rest) > 0 {
		if cmd.Rest == "" {
			return nil, fmt.Errorf("%s takes nothing after --", cmd.Command)
		}
		words := make([]any, len(rest))
		for i, w := range rest {
			words[i] = w
		}
		req[cmd.Rest] = words
	}
	return req, nil
}

func flagValue(typ, s string) (any, error) {
	switch typ {
	case "integer":
		return strconv.ParseInt(s, 10, 64)
	case "number":
		return strconv.ParseFloat(s, 64)
	case "boolean":
		return strconv.ParseBool(s)
	}
	return s, nil
}

func (a *App) cliHelp(w io.Writer, words []string) int {
	cmds := a.cliCommands()
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Command < cmds[j].Command })
	if len(words) >= 2 {
		if c, ok := a.cliByWords(words[0], words[1]); ok {
			fmt.Fprintf(w, "%s %s", c.Service, methodWord(c.Command))
			if c.Arg != "" {
				fmt.Fprintf(w, " <%s>", c.Arg)
			}
			if c.Rest != "" {
				fmt.Fprint(w, " [<args>...]")
			}
			fmt.Fprintf(w, "\n\n%s\n", c.Doc)
			flags := flagsOf(c.Schema)
			names := make([]string, 0, len(flags))
			for n, f := range flags {
				if f.field != c.Arg && f.field != c.Rest {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			if len(names) > 0 {
				fmt.Fprintln(w, "\nFlags:")
				for _, n := range names {
					f := flags[n]
					kind := f.typ
					if f.array {
						kind += ", repeatable"
					}
					fmt.Fprintf(w, "  --%s (%s)\n", n, kind)
				}
			}
			return ExitOK
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range cmds {
		if len(words) > 0 && c.Service != words[0] {
			continue
		}
		fmt.Fprintf(tw, "  %s %s\t%s\n", c.Service, methodWord(c.Command), firstLine(c.Doc))
	}
	tw.Flush()
	fmt.Fprintln(w, "\nGlobal: --json  --yes  --launch  --input <file|->  --timeout <d>    call <service:method> [<json>|-]    cli install|uninstall|status")
	return ExitOK
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func renderHuman(w io.Writer, res json.RawMessage) {
	res = bytes.TrimSpace(res)
	if len(res) == 0 || string(res) == "null" || string(res) == "{}" {
		return
	}
	keys, obj := orderedObject(res)
	if obj != nil {
		// Printed bare so `eval "$(<app> ...)"` works.
		if len(keys) == 1 {
			var text string
			if json.Unmarshal(obj[keys[0]], &text) == nil {
				fmt.Fprint(w, text)
				if !strings.HasSuffix(text, "\n") {
					fmt.Fprintln(w)
				}
				return
			}
		}
		if len(keys) == 1 {
			if rows := recordsOf(obj[keys[0]]); rows != nil {
				renderTable(w, rows)
				return
			}
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, k := range keys {
			fmt.Fprintf(tw, "%s\t%s\n", k, scalarText(obj[k]))
		}
		tw.Flush()
		return
	}
	if rows := recordsOf(res); rows != nil {
		renderTable(w, rows)
		return
	}
	fmt.Fprintln(w, scalarText(res))
}

type record struct {
	keys []string
	vals map[string]json.RawMessage
}

func recordsOf(raw json.RawMessage) []record {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	rows := make([]record, 0, len(items))
	for _, it := range items {
		keys, obj := orderedObject(it)
		if obj == nil {
			return nil
		}
		rows = append(rows, record{keys, obj})
	}
	return rows
}

func renderTable(w io.Writer, rows []record) {
	if len(rows) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	cols := rows[0].keys
	fmt.Fprintln(tw, strings.ToUpper(strings.Join(cols, "\t")))
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = scalarText(r.vals[c])
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	tw.Flush()
}

// The reply's own key order, which is the proto's: sorting puts id last.
func orderedObject(raw json.RawMessage) ([]string, map[string]json.RawMessage) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil
	}
	var keys []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil
		}
		keys = append(keys, k)
		vals[k] = v
	}
	return keys, vals
}

func scalarText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
