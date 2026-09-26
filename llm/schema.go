package llm

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// SchemaFor describes T the way encoding/json writes it, so a reply that
// satisfies the schema decodes into T. A field's `desc` tag becomes its
// description, which is most of what a model has to go on.
func SchemaFor[T any]() (json.RawMessage, error) {
	s, err := schemaOf(reflect.TypeFor[T](), map[reflect.Type]bool{})
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func ToolFor[T any](name, description string) (Tool, error) {
	s, err := SchemaFor[T]()
	if err != nil {
		return Tool{}, fmt.Errorf("tool %s: %w", name, err)
	}
	return Tool{Name: name, Description: description, Parameters: s}, nil
}

func FormatFor[T any](name string) (*ResponseFormat, error) {
	s, err := SchemaFor[T]()
	if err != nil {
		return nil, fmt.Errorf("format %s: %w", name, err)
	}
	return &ResponseFormat{Name: name, Schema: s}, nil
}

type schema struct {
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Format      string   `json:"format,omitempty"`
	Items       *schema  `json:"items,omitempty"`
	Properties  *props   `json:"properties,omitempty"`
	Required    []string `json:"required,omitempty"`
	// false for a struct, a schema for a map.
	AdditionalProperties any `json:"additionalProperties,omitempty"`
}

// Field order is kept because a model tends to fill properties in the order it
// reads them, and the struct's order is usually the one that makes sense.
type props struct {
	names []string
	of    map[string]*schema
}

func (p *props) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, n := range p.names {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(n)
		v, err := json.Marshal(p.of[n])
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

var (
	timeType          = reflect.TypeFor[time.Time]()
	rawType           = reflect.TypeFor[json.RawMessage]()
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

func schemaOf(t reflect.Type, visiting map[reflect.Type]bool) (*schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == timeType:
		return &schema{Type: "string", Format: "date-time"}, nil
	case t == rawType:
		return &schema{}, nil
	case implements(t, jsonMarshalerType):
		// It writes whatever it likes; claiming a shape would be a guess.
		return &schema{}, nil
	case implements(t, textMarshalerType):
		return &schema{Type: "string"}, nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return &schema{Type: "boolean"}, nil
	case reflect.String:
		return &schema{Type: "string"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &schema{Type: "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return &schema{Type: "number"}, nil
	case reflect.Interface:
		return &schema{}, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Kind() == reflect.Slice {
			return &schema{Type: "string"}, nil // encoding/json writes []byte as base64
		}
		items, err := schemaOf(t.Elem(), visiting)
		if err != nil {
			return nil, err
		}
		return &schema{Type: "array", Items: items}, nil
	case reflect.Map:
		switch k := t.Key(); {
		case k.Kind() == reflect.String, implements(k, textMarshalerType),
			k.Kind() >= reflect.Int && k.Kind() <= reflect.Uint64:
		default:
			return nil, fmt.Errorf("map key %s is not something encoding/json can write", k)
		}
		values, err := schemaOf(t.Elem(), visiting)
		if err != nil {
			return nil, err
		}
		return &schema{Type: "object", AdditionalProperties: values}, nil
	case reflect.Struct:
		if visiting[t] {
			return nil, fmt.Errorf("%s is recursive, which a schema without $ref cannot describe", t)
		}
		visiting[t] = true
		defer delete(visiting, t)
		s := &schema{Type: "object", Properties: &props{of: map[string]*schema{}}, AdditionalProperties: false}
		if err := addFields(s, t, visiting); err != nil {
			return nil, err
		}
		return s, nil
	}
	return nil, fmt.Errorf("%s has no JSON form", t)
}

func addFields(s *schema, t reflect.Type, visiting map[reflect.Type]bool) error {
	var order []string
	by := map[string][]candidate{}
	seq := 0
	collect(t, 0, &seq, map[reflect.Type]bool{}, &order, by)
	won := make([]candidate, 0, len(order))
	for _, name := range order {
		if c, ok := promote(by[name]); ok {
			won = append(won, c)
		}
	}
	// By the winner's own place, which is the order encoding/json writes. A
	// name first seen on a field that then lost would otherwise sit where the
	// loser was, and a model fills properties in the order it reads them.
	slices.SortStableFunc(won, func(a, b candidate) int { return a.seq - b.seq })
	for _, c := range won {
		fs, err := schemaOf(c.typ, visiting)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", t.Name(), c.field, err)
		}
		// encoding/json writes and reads a `,string` field quoted, so a schema
		// that names the Go type has the model answer with a number and Decode
		// refuse it on every call.
		if c.quoted {
			fs = &schema{Type: "string"}
		}
		fs.Description = c.desc
		s.Properties.names = append(s.Properties.names, c.name)
		s.Properties.of[c.name] = fs
		if !c.optional {
			s.Required = append(s.Required, c.name)
		}
	}
	return nil
}

// One field of the type, at the depth the embedding reached it.
type candidate struct {
	field    string
	name     string
	typ      reflect.Type
	desc     string
	depth    int
	seq      int
	tagged   bool
	optional bool
	quoted   bool
}

// What encoding/json does with a name more than one field answers to: the
// shallowest wins, and among equals a single tagged one wins, otherwise the
// name belongs to no field at all and is written and read by neither. A schema
// that promises it anyway has the model fill in something that lands nowhere,
// and Decode returns no error to say so.
func promote(cs []candidate) (candidate, bool) {
	best := cs[0]
	for _, c := range cs[1:] {
		if c.depth < best.depth {
			best = c
		}
	}
	var same, tagged []candidate
	for _, c := range cs {
		if c.depth == best.depth {
			same = append(same, c)
			if c.tagged {
				tagged = append(tagged, c)
			}
		}
	}
	switch {
	case len(same) == 1:
		return same[0], true
	case len(tagged) == 1:
		return tagged[0], true
	}
	return candidate{}, false
}

func collect(t reflect.Type, depth int, seq *int, path map[reflect.Type]bool, order *[]string, by map[string][]candidate) {
	if path[t] {
		return // a type embedded in itself; encoding/json stops here too
	}
	path[t] = true
	defer delete(path, t)
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		// encoding/json promotes the fields of an untagged embedded struct, and
		// does so even when the struct type itself is unexported.
		if f.Anonymous && name == "" && ft.Kind() == reflect.Struct {
			collect(ft, depth+1, seq, path, order, by)
			continue
		}
		// An embedded struct with a tag is an ordinary field named by that tag,
		// and encoding/json fills it even when the type is unexported - the one
		// carve-out in the export rule.
		if !f.IsExported() && !(f.Anonymous && ft.Kind() == reflect.Struct) {
			continue
		}
		tagged := name != ""
		if name == "" {
			name = f.Name
		}
		if _, seen := by[name]; !seen {
			*order = append(*order, name)
		}
		*seq++
		by[name] = append(by[name], candidate{
			field: f.Name, name: name, typ: f.Type, desc: f.Tag.Get("desc"), depth: depth, seq: *seq, tagged: tagged,
			// Whole options between the commas, the way encoding/json reads
			// them: "omitemptyish" is not omitempty. omitzero is omitempty's
			// younger name, and a required field the type omits is one a
			// grammar-compiling server makes the model invent.
			optional: hasOpt(opts, "omitempty") || hasOpt(opts, "omitzero"),
			quoted:   hasOpt(opts, "string"),
		})
	}
}

// Whole options between the commas, the way encoding/json reads them.
func hasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

func implements(t, iface reflect.Type) bool {
	return t.Implements(iface) || reflect.PointerTo(t).Implements(iface)
}
