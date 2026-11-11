package connect

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

type recorder struct{ contentType string }

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	r.contentType = req.Header.Get("Content-Type")
	return nil, errors.New("recorded")
}

func TestTheConnSpeaksTheProtocolItWasDialedWith(t *testing.T) {
	for grpc, want := range map[bool]string{true: "application/grpc", false: "application/proto"} {
		rec := &recorder{}
		Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), Dial("http://callee", ConnOptions{GRPC: grpc, HTTPClient: rec}), echo, wrapperspb.String("x"))
		if rec.contentType != want {
			t.Errorf("GRPC=%v sent %q, want %q", grpc, rec.contentType, want)
		}
	}
}

func TestGRPCToAnHTTPBaseGetsCleartextHTTP2(t *testing.T) {
	h2c := func(c *Conn) bool {
		hc, ok := c.opt.HTTPClient.(*http.Client)
		if !ok {
			return false
		}
		tr, ok := hc.Transport.(*http.Transport)
		return ok && tr.Protocols != nil && tr.Protocols.UnencryptedHTTP2()
	}
	if !h2c(Dial("http://callee:8070", ConnOptions{GRPC: true})) {
		t.Error("gRPC to http:// would go out as HTTP/1.1, which a grpc-go server refuses")
	}
	if h2c(Dial("https://callee", ConnOptions{GRPC: true})) || h2c(Dial("http://callee", ConnOptions{})) {
		t.Error("cleartext HTTP/2 was forced where TLS or HTTP/1.1 Connect applies")
	}
}

func TestOneClientServesEveryCallToAProcedure(t *testing.T) {
	c := Dial("http://callee", ConnOptions{HTTPClient: &recorder{}})
	for range 3 {
		Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), c, echo, wrapperspb.String("x"))
	}
	n := 0
	c.clients.Range(func(k, _ any) bool {
		if k != echo {
			t.Errorf("client cached under %v", k)
		}
		n++
		return true
	})
	if n != 1 {
		t.Fatalf("%d clients for one procedure", n)
	}
}
