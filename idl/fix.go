package idl

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tradalab/scorix/idl/scorixpb"
)

type rewrite struct {
	start, end int
	endCol     int
	indent     string
	drop       []int
	options    []string
	effect     bool
}

// Fix moves every @mcp, @cli, @event and @broadcast comment of the proto file name into its
// scorix.v1 option and returns the new text. Notes say where the option form behaves more
// carefully than the comment did; an error means nothing was rewritten.
func Fix(name, src string) (string, []string, error) {
	fd, err := compile(name, src)
	if err != nil {
		return "", nil, err
	}
	before, err := read(fd)
	if err != nil {
		return "", nil, err
	}
	eol := "\n"
	if strings.Contains(src, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	legacy := map[string][]string{}
	for _, s := range before.Services {
		for _, r := range s.RPCs {
			legacy[s.Name+"."+r.Name] = r.Legacy
		}
	}
	var edits []rewrite
	var notes []string
	careful, effected := map[string]bool{}, map[string]bool{}
	locs := fd.SourceLocations()
	for i := 0; i < fd.Services().Len(); i++ {
		s := fd.Services().Get(i)
		for j := 0; j < s.Methods().Len(); j++ {
			md := s.Methods().Get(j)
			e, note, err := plan(lines, locs.ByDescriptor(md), md)
			if err != nil {
				return "", nil, err
			}
			key := string(s.Name()) + "." + string(md.Name())
			if len(e.drop) == 0 && len(legacy[key]) > 0 {
				return "", nil, fmt.Errorf("proto: rpc %s has %s in a /* */ comment, which fix does not rewrite; move it into its scorix.v1 option by hand", key, strings.Join(legacy[key], " and "))
			}
			if len(e.options) > 0 {
				edits = append(edits, e)
				notes = append(notes, note...)
				careful[key] = len(note) > 0
				effected[key] = e.effect
			}
		}
	}
	if len(edits) == 0 {
		return src, nil, nil
	}
	slices.SortFunc(edits, func(a, b rewrite) int { return b.start - a.start })
	for _, e := range edits {
		lines = apply(lines, e)
	}
	if !imports(fd) {
		lines = withImport(lines)
	}
	out := strings.Join(lines, eol)
	if err := same(name, before, out, careful, effected); err != nil {
		return "", nil, err
	}
	return out, notes, nil
}

func imports(fd protoreflect.FileDescriptor) bool {
	for i := 0; i < fd.Imports().Len(); i++ {
		if fd.Imports().Get(i).Path() == traitPath {
			return true
		}
	}
	return false
}

func withImport(lines []string) []string {
	at := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "package ") || (at < 0 && strings.HasPrefix(t, "syntax ")) {
			at = i
		}
	}
	return slices.Insert(lines, at+1, "", `import "`+traitPath+`";`)
}

func plan(lines []string, loc protoreflect.SourceLocation, md protoreflect.MethodDescriptor) (rewrite, []string, error) {
	e := rewrite{start: loc.StartLine, end: loc.EndLine, endCol: loc.EndColumn}
	e.indent = lines[e.start][:len(lines[e.start])-len(strings.TrimLeft(lines[e.start], " \t"))]
	where := fmt.Sprintf("%s.%s", md.Parent().Name(), md.Name())
	var mcp, cli, event string
	var mcpCareful, cliCareful, destructive bool
	first := strings.TrimSpace(lines[e.start][:byteOffset(lines[e.start], loc.StartColumn)]) == ""
	for i := e.start - 1; first && i >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i]), "//"); i-- {
		line := lines[i]
		switch {
		case mcpRe.MatchString(line):
			mods := strings.Fields(mcpRe.FindStringSubmatch(line)[1])
			mcp, mcpCareful = "{}", true
			for _, m := range mods {
				if m == "destructive" {
					destructive, mcpCareful = true, false
				} else if t, ok := strings.CutPrefix(m, "timeout="); ok {
					mcp = fmt.Sprintf(`{ timeout: %q }`, t)
				}
			}
		case cliRe.MatchString(line):
			cliCareful = true
			var fields []string
			vals := map[string]string{}
			for _, m := range strings.Fields(cliRe.FindStringSubmatch(line)[1]) {
				switch m {
				case "destructive":
					destructive, cliCareful = true, false
				case "headless", "exec":
					vals[m] = "true"
				default:
					k, v, _ := strings.Cut(m, "=")
					vals[k] = fmt.Sprintf("%q", v)
				}
			}
			for _, k := range []string{"headless", "exec", "arg", "rest", "secret", "timeout"} {
				if v, ok := vals[k]; ok {
					fields = append(fields, k+": "+v)
				}
			}
			cli = "{}"
			if len(fields) > 0 {
				cli = "{ " + strings.Join(fields, " ") + " }"
			}
		case eventRe.MatchString(line):
			event = "{}"
			if eventRe.FindStringSubmatch(line)[1] == "in" {
				event = "{ direction: IN }"
			}
		case broadcastRe.MatchString(line):
			event = "{}"
		default:
			continue
		}
		e.drop = append(e.drop, i)
	}
	if mcp == "" && cli == "" && event == "" {
		return e, nil, nil
	}
	rule, effect, err := declared(md)
	if err != nil {
		return e, nil, err
	}
	if destructive && effect != scorixpb.Effect_DESTRUCTIVE {
		if rule != nil {
			return e, nil, fmt.Errorf("proto: rpc %s has a (scorix.v1.rpc) option; add effect: DESTRUCTIVE to it by hand, then run fix again", where)
		}
		e.options = append(e.options, "option (scorix.v1.rpc) = { effect: DESTRUCTIVE };")
		e.effect, effect = true, scorixpb.Effect_DESTRUCTIVE
	}
	var notes []string
	if effect == scorixpb.Effect_DESTRUCTIVE && mcp != "" && mcpCareful {
		notes = append(notes, fmt.Sprintf("rpc %s: its effect is DESTRUCTIVE, so as (scorix.v1.mcp) agents are told it is destructive", where))
	}
	if effect == scorixpb.Effect_DESTRUCTIVE && cli != "" && cliCareful {
		notes = append(notes, fmt.Sprintf("rpc %s: its effect is DESTRUCTIVE, so as (scorix.v1.cli) it asks before it runs", where))
	}
	for _, o := range []struct{ trait, value string }{{"mcp", mcp}, {"cli", cli}, {"event", event}} {
		if o.value != "" {
			e.options = append(e.options, fmt.Sprintf("option (scorix.v1.%s) = %s;", o.trait, o.value))
		}
	}
	return e, notes, nil
}

func apply(lines []string, e rewrite) []string {
	last := lines[e.end]
	end := byteOffset(last, e.endCol)
	head, tail := last[:end-1], last[end:]
	inner := e.indent + "  "
	body := slices.Clone(lines[e.start:e.end])
	switch openLine, openCol := bodyOpen(lines, e); {
	case last[end-1] == ';':
		body = append(body, head+" {")
	case openLine == e.end:
		body = append(body, strings.TrimRight(head[:openCol+1], " "))
		if stmt := strings.TrimSpace(head[openCol+1:]); stmt != "" {
			body = append(body, inner+stmt)
		}
	default:
		if stmt := strings.TrimSpace(head); stmt != "" {
			body = append(body, inner+stmt)
		}
	}
	for _, o := range e.options {
		body = append(body, inner+o)
	}
	body = append(body, e.indent+"}"+tail)
	out := slices.Concat(lines[:e.start], body, lines[e.end+1:])
	slices.Sort(e.drop)
	for k := len(e.drop) - 1; k >= 0; k-- {
		out = slices.Delete(out, e.drop[k], e.drop[k]+1)
	}
	return out
}

func byteOffset(line string, col int) int {
	c := 0
	for i := 0; i < len(line); i++ {
		if c >= col {
			return i
		}
		if line[i] == '\t' {
			c += 8 - c%8
		} else if utf8.RuneStart(line[i]) {
			c++
		}
	}
	return len(line)
}

func bodyOpen(lines []string, e rewrite) (int, int) {
	seen := false
	depth := 0
	for i := e.start; i <= e.end; i++ {
		line := lines[i]
		from := 0
		if !seen {
			k := strings.Index(line, "returns")
			if k < 0 {
				continue
			}
			seen, from = true, k
		}
		for c := from; c < len(line); c++ {
			switch line[c] {
			case '(':
				depth++
			case ')':
				depth--
			case '{':
				if depth == 0 {
					return i, c
				}
			}
		}
	}
	return -1, -1
}

func same(name string, before File, out string, careful, effected map[string]bool) error {
	after, err := Parse(name, out)
	if err != nil {
		return fmt.Errorf("fix wrote a proto that does not compile, so nothing was written: %w", err)
	}
	for i := range before.Services {
		for j := range before.Services[i].RPCs {
			b := &before.Services[i].RPCs[j]
			b.Legacy = nil
			key := before.Services[i].Name + "." + b.Name
			if careful[key] {
				b.MCPDestructive, b.CLIDestructive = b.MCP, b.CLI
			}
			if effected[key] {
				b.Effect = "destructive"
			}
		}
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("fix would change what %s means beyond the notes, so nothing was written", name)
	}
	return nil
}
