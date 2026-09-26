package llm

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type schemaAddress struct {
	City string `json:"city" desc:"City name"`
	Zip  string `json:"zip,omitempty"`
}

type schemaNote struct {
	Note string `json:"note"`
}

type schemaPerson struct {
	Name    string             `json:"name" desc:"Full name"`
	Age     int                `json:"age"`
	Tags    []string           `json:"tags,omitempty"`
	Address *schemaAddress     `json:"address,omitempty"`
	Scores  map[string]float64 `json:"scores"`
	Skip    string             `json:"-"`
	hidden  int
	Born    time.Time       `json:"born"`
	Raw     json.RawMessage `json:"raw,omitempty"`
	Any     any             `json:"any,omitempty"`
	Photo   []byte          `json:"photo,omitempty"`
	Plain   bool
	schemaNote
}

func sameJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("not JSON: %s", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("schema =\n%s\nwant\n%s", got, want)
	}
}

// The schema has to describe what encoding/json writes, field names and all,
// or a reply that satisfies it still fails to decode into the type.
func TestSchemaFollowsEncodingJSON(t *testing.T) {
	_ = schemaPerson{}.hidden
	got, err := SchemaFor[schemaPerson]()
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, `{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Full name"},
			"age": {"type": "integer"},
			"tags": {"type": "array", "items": {"type": "string"}},
			"address": {
				"type": "object",
				"properties": {"city": {"type": "string", "description": "City name"}, "zip": {"type": "string"}},
				"required": ["city"],
				"additionalProperties": false
			},
			"scores": {"type": "object", "additionalProperties": {"type": "number"}},
			"born": {"type": "string", "format": "date-time"},
			"raw": {},
			"any": {},
			"photo": {"type": "string"},
			"Plain": {"type": "boolean"},
			"note": {"type": "string"}
		},
		"required": ["name", "age", "scores", "born", "Plain", "note"],
		"additionalProperties": false
	}`)
}

func TestSchemaKeepsFieldOrder(t *testing.T) {
	got, err := SchemaFor[schemaAddress]()
	if err != nil {
		t.Fatal(err)
	}
	if i, j := strings.Index(string(got), `"city"`), strings.Index(string(got), `"zip"`); i < 0 || j < i {
		t.Errorf("properties out of field order: %s", got)
	}
}

type schemaNode struct {
	Value int         `json:"value"`
	Next  *schemaNode `json:"next,omitempty"`
}

// Without $ref a recursive type expands forever; refusing is better than a
// schema that silently stops at some depth.
func TestSchemaRefusesWhatItCannotDescribe(t *testing.T) {
	if _, err := SchemaFor[schemaNode](); err == nil || !strings.Contains(err.Error(), "recursive") {
		t.Errorf("recursive type: %v", err)
	}
	if _, err := SchemaFor[struct{ C chan int }](); err == nil {
		t.Error("a channel field was described")
	}
	if _, err := SchemaFor[map[int]string](); err != nil {
		t.Errorf("integer map keys are JSON object keys: %v", err)
	}
	if _, err := SchemaFor[map[bool]string](); err == nil {
		t.Error("a map encoding/json cannot write was described")
	}
}

func TestToolForAndFormatForCarryTheSchema(t *testing.T) {
	tool, err := ToolFor[schemaAddress]("lookup", "Find an address")
	if err != nil {
		t.Fatal(err)
	}
	f, err := FormatFor[schemaAddress]("address")
	if err != nil {
		t.Fatal(err)
	}
	if tool.Name != "lookup" || tool.Description != "Find an address" || f.Name != "address" || string(tool.Parameters) != string(f.Schema) {
		t.Errorf("tool %+v, format %+v", tool, f)
	}
	var a schemaAddress
	if err := (ToolCall{Name: "lookup", Arguments: `{"city":"Hue"}`}).Decode(&a); err != nil || a.City != "Hue" {
		t.Errorf("decode = %+v, %v", a, err)
	}
	if err := (ToolCall{Name: "lookup", Arguments: `{"city":`}).Decode(&a); err == nil || !strings.Contains(err.Error(), "lookup") {
		t.Errorf("broken arguments: %v", err)
	}
}

type MetaName struct {
	Name string `json:"name"`
}

type AuditName struct {
	Name string `json:"name"`
	When string `json:"when"`
}

// go vet refuses a literal struct whose embedded types promote the same json
// name, which is exactly the shape under test, so this one is built at run
// time. Nothing stops a caller writing it - vet is a warning, not a compiler.
func twoPromotedNames() reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "MetaName", Type: reflect.TypeFor[MetaName](), Anonymous: true},
		{Name: "AuditName", Type: reflect.TypeFor[AuditName](), Anonymous: true},
		{Name: "Body", Type: reflect.TypeFor[string](), Tag: `json:"body"`},
	})
}

type innerID struct {
	ID string `json:"id"`
}

type outerID struct {
	innerID
	ID int `json:"id"`
}

// The promise this package makes is that a reply satisfying the schema decodes
// into T. encoding/json drops a name two embedded structs promote at the same
// depth and prefers the shallower of two at different depths, so a schema that
// promises the field anyway has the model fill in something that lands nowhere,
// with Decode returning no error at all.
func TestPromotedFieldsFollowWhatEncodingJSONDoes(t *testing.T) {
	both := twoPromotedNames()
	sch, err := schemaOf(both, map[reflect.Type]bool{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sch)
	if err != nil {
		t.Fatal(err)
	}
	// Measured against what encoding/json does, not against the same rule
	// written out a second time.
	written, err := json.Marshal(reflect.New(both).Interface())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), `"name"`) {
		t.Fatalf("encoding/json writes name after all, so the schema should promise it: %s", written)
	}
	var got struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, promised := got.Properties["name"]; promised || slices.Contains(got.Required, "name") {
		t.Errorf("a name neither struct wins is still promised: %s", raw)
	}
	if _, ok := got.Properties["when"]; !ok {
		t.Errorf("a name only one struct promotes was dropped: %s", raw)
	}
	if _, ok := got.Properties["body"]; !ok {
		t.Errorf("the outer field was dropped: %s", raw)
	}

	raw, err = SchemaFor[outerID]()
	if err != nil {
		t.Fatal(err)
	}
	got.Properties = nil
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Properties["id"].Type != "integer" {
		t.Errorf("the embedded id shadowed the outer one: %s", raw)
	}
}

type omitzeroFields struct {
	Always string `json:"always"`
	Maybe  string `json:"maybe,omitzero"`
}

// omitzero is omitempty's younger name, and a required field the type omits is
// one a grammar-compiling server forces the model to invent.
func TestOmitzeroIsNotRequired(t *testing.T) {
	raw, err := SchemaFor[omitzeroFields]()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Required, []string{"always"}) {
		t.Errorf("required = %v", got.Required)
	}
}

type quotedNumber struct {
	N int  `json:"n,string"`
	B bool `json:"b,string"`
	P int  `json:"p"`
}

type oddOption struct {
	Always string `json:"always"`
	Weird  string `json:"weird,omitemptyish"`
}

type hiddenInner struct {
	Hidden string `json:"hidden"`
}

type taggedHidden struct {
	hiddenInner `json:"inner"`
	Own         string `json:"own"`
}

type deeper struct {
	Deep string `json:"deep"`
}

type outerWins struct {
	A string `json:"a"`
	deeper
	B    string `json:"b"`
	Deep string `json:"deep"`
}

// encoding/json writes and reads these as quoted strings, so a schema that
// names the Go type has the model answer with a number and Decode refuse it -
// on every single call, with a type error the app author cannot act on.
func TestAQuotedNumberIsDescribedAsAString(t *testing.T) {
	raw, err := SchemaFor[quotedNumber]()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Properties["n"].Type != "string" || got.Properties["b"].Type != "string" || got.Properties["p"].Type != "integer" {
		t.Errorf("properties = %+v", got.Properties)
	}
}

// encoding/json matches whole options between the commas. Reading the tag as
// one long string makes an option nobody wrote look like omitempty, and the
// model is told it may skip a field the type always writes.
func TestAnOptionThatMerelyStartsLikeOmitemptyIsNotOne(t *testing.T) {
	raw, err := SchemaFor[oddOption]()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Required, []string{"always", "weird"}) {
		t.Errorf("required = %v", got.Required)
	}
}

// An embedded struct with a tag is an ordinary field named by that tag, and
// encoding/json fills it even when the type is unexported. Leaving it out of
// the schema means the model is never told about it - and with
// additionalProperties false, a strict server forbids it.
func TestATaggedEmbeddedStructIsAField(t *testing.T) {
	raw, err := SchemaFor[taggedHidden]()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Properties["inner"]; !ok {
		t.Errorf("properties = %v", got.Properties)
	}
	// What encoding/json really does with it, so the schema is checked against
	// the behaviour rather than against the same rule written twice.
	var v taggedHidden
	if err := json.Unmarshal([]byte(`{"inner":{"hidden":"q"},"own":"o"}`), &v); err != nil || v.Hidden != "q" {
		t.Errorf("decode gave %+v, %v", v, err)
	}
}

// A model fills properties in the order it reads them, which is why the order
// is kept at all. It has to be the order encoding/json writes: the winner's
// place, not the place a candidate that lost was first seen.
func TestPropertyOrderFollowsTheFieldThatWins(t *testing.T) {
	raw, err := SchemaFor[outerWins]()
	if err != nil {
		t.Fatal(err)
	}
	written, err := json.Marshal(outerWins{})
	if err != nil {
		t.Fatal(err)
	}
	if got := propertyOrder(t, raw); !slices.Equal(got, []string{"a", "b", "deep"}) {
		t.Errorf("schema order = %v, encoding/json writes %s", got, written)
	}
}

// The keys of the properties object, in the order they are written.
func propertyOrder(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var doc struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(doc.Properties))
	if _, err := dec.Token(); err != nil { // the opening brace
		t.Fatal(err)
	}
	var names []string
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, key.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return names
}
