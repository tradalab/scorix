package confvalue

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func assign[T any](t *testing.T, v any) T {
	t.Helper()
	var out T
	if err := Assign(reflect.ValueOf(&out).Elem(), v); err != nil {
		t.Fatalf("Assign(%T, %#v): %v", out, v, err)
	}
	return out
}

func TestADurationIsReadAsTextNotAsNanoseconds(t *testing.T) {
	if d := assign[time.Duration](t, "30s"); d != 30*time.Second {
		t.Fatalf("\"30s\" = %v", d)
	}
}

func TestDecodedNumbersArriveAsFloat64AndStillFillIntsAndBools(t *testing.T) {
	if n := assign[int](t, float64(42)); n != 42 {
		t.Fatalf("int from float64 = %d", n)
	}
	if !assign[bool](t, float64(1)) || assign[bool](t, float64(0)) {
		t.Fatal("bool from float64")
	}
}

func TestYAMLBoolSpellingsLoadAlongsideStrconvOnes(t *testing.T) {
	for in, want := range map[string]bool{"yes": true, "On": true, "y": true, "no": false, "off": false, "N": false, "1": true, "f": false, "true": true} {
		if got := assign[bool](t, in); got != want {
			t.Errorf("%q = %v, want %v", in, got, want)
		}
	}
}

func TestAStringListIsACommaSeparatedEnvValueOrAYAMLList(t *testing.T) {
	for in, want := range map[any]string{"a, b ,c": "a|b|c", "  ": ""} {
		if got := strings.Join(assign[[]string](t, in), "|"); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
	if got := strings.Join(assign[[]string](t, []any{"x", 2}), "|"); got != "x|2" {
		t.Errorf("YAML list = %q", got)
	}
}

func TestAnIntegerTooBigForItsFieldIsRefused(t *testing.T) {
	var i16 int16
	var u8 uint8
	var i8 int8
	for _, c := range []struct {
		dst any
		v   any
	}{{&i16, "70000"}, {&i16, "-40000"}, {&u8, "256"}, {&u8, float64(300)}, {&i8, int64(128)}} {
		if err := Assign(reflect.ValueOf(c.dst).Elem(), c.v); err == nil || !strings.Contains(err.Error(), "overflows") {
			t.Errorf("%v into %T: %v, want an overflow", c.v, c.dst, err)
		}
	}
	if n := assign[int16](t, "32767"); n != 32767 {
		t.Fatalf("int16 max = %d", n)
	}
	if n := assign[uint8](t, "255"); n != 255 {
		t.Fatalf("uint8 max = %d", n)
	}
}
