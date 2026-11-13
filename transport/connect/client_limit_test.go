package connect

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/kernel"
)

func TestAReplyOverTheClientReadCapIsResourceExhaustedAndALargerCapLetsItThrough(t *testing.T) {
	m := mux(t, Options{Auth: trustAll, ReadMaxBytes: 16 << 20})
	Unary(m, echo, kernel.Rule{}, ok)
	ServerStream(m, count, kernel.Rule{}, func(_ context.Context, in *msg, send func(*msg) error) error {
		return send(in)
	})
	srv := tlsServer(t, m)
	ctx := context.Background()
	small, big := strings.Repeat("x", 3<<20), strings.Repeat("x", 5<<20)
	for _, grpc := range []bool{false, true} {
		for _, c := range []struct {
			name    string
			max     int
			text    string
			refused bool
		}{
			{"default, 3 MiB", 0, small, false},
			{"default, 5 MiB", 0, big, true},
			{"negative, 5 MiB", -1, big, true},
			{"raised to 8 MiB, 5 MiB", 8 << 20, big, false},
		} {
			conn := Dial(srv.URL, ConnOptions{GRPC: grpc, HTTPClient: srv.Client(), ReadMaxBytes: c.max})
			_, unary := Call[msg, msg](ctx, conn, echo, wrapperspb.String(c.text))
			stream := Stream(ctx, conn, count, wrapperspb.String(c.text), func(*msg) error { return nil })
			for kind, err := range map[string]error{"unary": unary, "stream": stream} {
				if c.refused && connect.CodeOf(err) != connect.CodeResourceExhausted {
					t.Errorf("grpc=%v %s %s = %v, want resource_exhausted", grpc, kind, c.name, err)
				}
				if !c.refused && err != nil {
					t.Errorf("grpc=%v %s %s = %v, want it through", grpc, kind, c.name, err)
				}
			}
		}
	}
}
