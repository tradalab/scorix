package rest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Marshal writes msg as JSON with every field present: an unset message is an object of zero
// values, or null where it would recurse, int64 is a number rather than a string, an enum is
// its name (its number when unknown), and a map is an object with sorted keys.
func Marshal(msg proto.Message) ([]byte, error) {
	var b bytes.Buffer
	err := message(&b, msg.ProtoReflect(), true, map[protoreflect.FullName]bool{})
	return b.Bytes(), err
}

func message(b *bytes.Buffer, m protoreflect.Message, set bool, open map[protoreflect.FullName]bool) error {
	name := m.Descriptor().FullName()
	if !set && open[name] {
		b.WriteString("null")
		return nil
	}
	if !open[name] {
		open[name] = true
		defer delete(open, name)
	}
	b.WriteByte('{')
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(f.JSONName())
		b.Write(key)
		b.WriteByte(':')
		var err error
		switch {
		case f.IsMap():
			err = object(b, f.MapValue(), m.Get(f).Map(), open)
		case f.Cardinality() == protoreflect.Repeated:
			err = list(b, f, m.Get(f).List(), open)
		default:
			err = value(b, f, m.Get(f), m.Has(f), open)
		}
		if err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

func list(b *bytes.Buffer, f protoreflect.FieldDescriptor, l protoreflect.List, open map[protoreflect.FullName]bool) error {
	b.WriteByte('[')
	for i := 0; i < l.Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := value(b, f, l.Get(i), true, open); err != nil {
			return err
		}
	}
	b.WriteByte(']')
	return nil
}

func object(b *bytes.Buffer, f protoreflect.FieldDescriptor, m protoreflect.Map, open map[protoreflect.FullName]bool) error {
	type entry struct {
		key string
		v   protoreflect.Value
	}
	entries := make([]entry, 0, m.Len())
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		entries = append(entries, entry{k.String(), v})
		return true
	})
	slices.SortFunc(entries, func(x, y entry) int { return strings.Compare(x.key, y.key) })
	b.WriteByte('{')
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(e.key)
		b.Write(key)
		b.WriteByte(':')
		if err := value(b, f, e.v, true, open); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

func value(b *bytes.Buffer, f protoreflect.FieldDescriptor, v protoreflect.Value, set bool, open map[protoreflect.FullName]bool) error {
	switch f.Kind() {
	case protoreflect.MessageKind:
		return message(b, v.Message(), set, open)
	case protoreflect.StringKind:
		s, err := json.Marshal(v.String())
		b.Write(s)
		return err
	case protoreflect.BytesKind:
		s, _ := json.Marshal(base64.StdEncoding.EncodeToString(v.Bytes()))
		b.Write(s)
	case protoreflect.BoolKind:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case protoreflect.EnumKind:
		if ev := f.Enum().Values().ByNumber(v.Enum()); ev != nil {
			s, _ := json.Marshal(string(ev.Name()))
			b.Write(s)
		} else {
			b.WriteString(strconv.FormatInt(int64(v.Enum()), 10))
		}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind, protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		b.WriteString(strconv.FormatUint(v.Uint(), 10))
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		s, err := json.Marshal(v.Float())
		b.Write(s)
		return err
	}
	return nil
}
