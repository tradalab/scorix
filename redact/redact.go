// Package redact copies a protobuf message with the fields its contract classifies
// (scorix.v1.field class) taken out, so a log line, an audit record or an agent never sees them.
package redact

import (
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/tradalab/scorix/idl/scorixpb"
)

// Masked is what a PII string reads in a log or audit record.
const Masked = "[pii]"

var classes sync.Map

func classOf(fd protoreflect.FieldDescriptor) scorixpb.Class {
	if c, ok := classes.Load(fd); ok {
		return c.(scorixpb.Class)
	}
	c := scorixpb.Class_CLASS_UNSPECIFIED
	if opts := fd.Options(); opts != nil {
		if f, ok := proto.GetExtension(opts, scorixpb.E_Field).(*scorixpb.Field); ok {
			c = f.GetClass()
		}
	}
	classes.Store(fd, c)
	return c
}

// Log is a copy of msg for a log line or an audit record: SECRET fields cleared, PII strings
// read Masked and other PII fields cleared. msg itself is left as it was.
func Log(msg proto.Message) proto.Message { return clean(msg, true) }

// Agent is a copy of msg for an agent: SECRET fields cleared, PII kept, since an agent acts
// for the person the data belongs to.
func Agent(msg proto.Message) proto.Message { return clean(msg, false) }

func clean(msg proto.Message, maskPII bool) proto.Message {
	if msg == nil {
		return nil
	}
	out := proto.Clone(msg)
	walk(out.ProtoReflect(), maskPII)
	return out
}

func walk(m protoreflect.Message, maskPII bool) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch classOf(fd) {
		case scorixpb.Class_SECRET:
			m.Clear(fd)
			return true
		case scorixpb.Class_PII:
			if maskPII {
				if fd.Kind() == protoreflect.StringKind && !fd.IsList() {
					m.Set(fd, protoreflect.ValueOfString(Masked))
				} else {
					m.Clear(fd)
				}
				return true
			}
		}
		switch {
		case fd.IsMap():
			if fd.MapValue().Kind() == protoreflect.MessageKind {
				v.Map().Range(func(_ protoreflect.MapKey, e protoreflect.Value) bool {
					walk(e.Message(), maskPII)
					return true
				})
			}
		case fd.Kind() != protoreflect.MessageKind:
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				walk(l.Get(i).Message(), maskPII)
			}
		case fd.Message().FullName() == anyName:
			if !opened(v.Message(), maskPII) {
				m.Clear(fd)
			}
		default:
			walk(v.Message(), maskPII)
		}
		return true
	})
}

const anyName = "google.protobuf.Any"

func opened(a protoreflect.Message, maskPII bool) bool {
	box, ok := a.Interface().(*anypb.Any)
	if !ok {
		return false
	}
	inner, err := box.UnmarshalNew()
	if err != nil {
		return false
	}
	walk(inner.ProtoReflect(), maskPII)
	return box.MarshalFrom(inner) == nil
}
