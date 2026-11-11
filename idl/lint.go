package idl

import (
	"fmt"
	"regexp"
)

// Lint is a place where a proto that compiles still strays from the Google AIPs the suite
// follows; Rule names the AIP, such as "aip-140".
type Lint struct {
	Rule  string
	Where string
	What  string
}

func (l Lint) String() string { return fmt.Sprintf("%s: %s %s", l.Rule, l.Where, l.What) }

var (
	snakeRe = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	camelRe = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

var aip158Names = map[string]string{
	"cursor":      "page_token",
	"next_cursor": "next_page_token",
	"limit":       "page_size",
	"total":       "total_size",
}

// LintFile lists every Lint in f, those of messages before those of rpcs, each in f's order.
func LintFile(f File) []Lint {
	var out []Lint
	add := func(rule, where, what string, args ...any) {
		out = append(out, Lint{Rule: rule, Where: where, What: fmt.Sprintf(what, args...)})
	}
	msgs := messagesByName(f)
	for _, m := range f.Messages {
		where := "message " + m.Name
		if !camelRe.MatchString(m.Name) {
			add("aip-190", where, "is not UpperCamelCase")
		}
		for _, fl := range m.Fields {
			if !snakeRe.MatchString(fl.Name) {
				add("aip-140", where, "field %s is not lower_snake_case", fl.Name)
			}
		}
	}
	for _, s := range f.Services {
		for _, r := range append(append([]RPC{}, s.RPCs...), s.Events...) {
			where := "rpc " + s.Name + "." + r.Name
			if !camelRe.MatchString(r.Name) {
				add("aip-190", where, "is not UpperCamelCase")
			}
			if r.HTTP == nil || !verb(r.Name, "List") || compound(r.Name) {
				continue
			}
			if r.List == nil {
				add("aip-158", where, "lists behind a REST route without (scorix.v1.list), so the server cannot page, filter or narrow it")
			}
			for _, m := range []Message{msgs[r.RequestType], msgs[r.ResponseType]} {
				for _, fl := range m.Fields {
					if want, ok := aip158Names[fl.Name]; ok {
						add("aip-158", where, "%s.%s is %s in AIP-158", m.Name, fl.Name, want)
					}
				}
			}
		}
	}
	return out
}
