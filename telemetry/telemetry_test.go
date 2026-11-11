package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/observe"
)

type rig struct {
	t      *Telemetry
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

func newRig(t *testing.T) rig {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	tel, err := New(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)), sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	return rig{tel, spans, reader}
}

func (r rig) metrics(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func attr(set attribute.Set, key string) string {
	v, _ := set.Value(attribute.Key(key))
	return v.String()
}

func call(procedure string, h http.Header) kernel.Call {
	if h == nil {
		h = http.Header{}
	}
	return kernel.Call{Procedure: procedure, Header: h}
}

func TestACallIsAServerSpanNamedByItsProcedureWithItsCode(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		err  error
		code string
		bad  bool
	}{
		{nil, "ok", false},
		{fault.New(fault.CodeNotFound, "gone"), "not_found", false},
		{connect.NewError(connect.CodeInternal, errors.New(`pq: relation "users" does not exist`)), "internal", true},
		{context.DeadlineExceeded, "deadline_exceeded", true},
	} {
		_, done := r.t.Start(context.Background(), "connect", call("/shorten.Rpc/GetLink", nil))
		done(c.err)
		spans := r.spans.Ended()
		s := spans[len(spans)-1]
		set := attribute.NewSet(s.Attributes()...)
		code, system, service, method := attr(set, "rpc.response.status_code"), attr(set, "rpc.system"), attr(set, "rpc.service"), attr(set, "rpc.method")
		if s.Name() != "shorten.Rpc/GetLink" || s.SpanKind() != trace.SpanKindServer || code != c.code || system != "connect_rpc" || service != "shorten.Rpc" || method != "GetLink" {
			t.Errorf("%v: span %s kind %v code %s system %s %s/%s", c.err, s.Name(), s.SpanKind(), code, system, service, method)
		}
		if (s.Status().Code == codes.Error) != c.bad {
			t.Errorf("%v: status %v; only a fault of the server marks the span as an error", c.err, s.Status())
		}
		if c.bad && s.Status().Description != c.code {
			t.Errorf("%v: status says %q; the trace backend gets the code, not the error text", c.err, s.Status().Description)
		}
	}
	_, done := r.t.Start(context.Background(), "rest", call("/shorten.Rpc/ResolveSlug", nil))
	done(nil)
	spans := r.spans.Ended()
	if v := attr(attribute.NewSet(spans[len(spans)-1].Attributes()...), "rpc.system"); v != "rest" {
		t.Errorf("rest call has rpc.system %s", v)
	}
}

func TestASpanJoinsTheTraceTheCallerSentAndPassesItOn(t *testing.T) {
	r := newRig(t)
	ctx, done := r.t.Start(context.Background(), "rest", call("/shorten.Rpc/ListLinks", nil))
	out := http.Header{}
	r.t.Inject(ctx, out)
	if !strings.HasPrefix(out.Get("Traceparent"), "00-") {
		t.Fatalf("Inject wrote %v", out)
	}
	_, inner := r.t.Start(context.Background(), "connect", call("/shorten.Rpc/ListLinks", out))
	inner(nil)
	done(nil)
	spans := r.spans.Ended()
	child, parent := spans[0], spans[1]
	if child.Parent().SpanID() != parent.SpanContext().SpanID() || child.SpanContext().TraceID() != parent.SpanContext().TraceID() {
		t.Fatalf("the callee's span is not the child of the caller's: %v under %v", child.Parent(), parent.SpanContext())
	}
}

func TestLogLinesOfACallCarryItsTraceID(t *testing.T) {
	r := newRig(t)
	var buf bytes.Buffer
	ctx := observe.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))
	ctx, done := r.t.Start(ctx, "connect", call("/x.Y/Z", nil))
	observe.Log(ctx).Info("inside")
	done(nil)
	id := r.spans.Ended()[0].SpanContext().TraceID().String()
	if !strings.Contains(buf.String(), `"trace_id":"`+id+`"`) || !strings.Contains(buf.String(), `"span_id":"`) {
		t.Fatalf("log line %s lacks trace %s", buf.String(), id)
	}
}

func TestEveryCallIsTimedCountedWhileActiveAndShedCallsAreCounted(t *testing.T) {
	r := newRig(t)
	_, a := r.t.Start(context.Background(), "connect", call("/x.Y/Get", nil))
	_, b := r.t.Start(context.Background(), "connect", call("/x.Y/Get", nil))
	a(nil)
	m := r.metrics(t)
	active := m["scorix.server.active_calls"].Data.(metricdata.Sum[int64])
	if len(active.DataPoints) != 1 || active.DataPoints[0].Value != 1 {
		t.Fatalf("active calls with one still running = %+v", active.DataPoints)
	}
	b(fault.New(fault.CodeNotFound, "gone"))
	req := httptest.NewRequest("POST", "/x.Y/Get", nil)
	r.t.Shed(req)
	r.t.Shed(req)
	m = r.metrics(t)
	if v := m["scorix.server.active_calls"].Data.(metricdata.Sum[int64]).DataPoints[0].Value; v != 0 {
		t.Errorf("active calls after both ended = %d", v)
	}
	if v := m["scorix.server.shed_calls"].Data.(metricdata.Sum[int64]).DataPoints[0].Value; v != 2 {
		t.Errorf("shed calls = %d", v)
	}
	if d := m["scorix.server.shed_calls"].Description; !strings.Contains(d, "MaxInFlight") || !strings.Contains(d, "ShedAboveCPU") {
		t.Errorf("shed calls described as %q; both MaxInFlight and ShedAboveCPU feed it", d)
	}
	codes := map[string]uint64{}
	for _, p := range m["rpc.server.call.duration"].Data.(metricdata.Histogram[float64]).DataPoints {
		codes[attr(p.Attributes, "rpc.response.status_code")+" "+attr(p.Attributes, "rpc.method")] = p.Count
	}
	if codes["ok Get"] != 1 || codes["not_found Get"] != 1 || len(codes) != 2 {
		t.Errorf("durations by code = %v", codes)
	}
}

type collector struct {
	*httptest.Server
	mu    sync.Mutex
	paths map[string]int
}

func newCollector(t *testing.T) *collector {
	c := &collector{paths: map[string]int{}}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		c.mu.Lock()
		c.paths[r.URL.Path]++
		c.mu.Unlock()
	}))
	t.Cleanup(c.Close)
	return c
}

func TestStartExportsBothSignalsOverOTLPWhenAnEndpointIsSet(t *testing.T) {
	c := newCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", c.URL)
	tel, err := Start(context.Background(), Config{Service: "shorten-rpc", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	_, done := tel.Start(context.Background(), "connect", call("/x.Y/Z", nil))
	done(nil)
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paths["/v1/traces"] == 0 || c.paths["/v1/metrics"] == 0 {
		t.Fatalf("the collector got %v", c.paths)
	}
}

func TestStartWithoutAnEndpointExportsNothingAndRefusesWhatItCannotSpeak(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	tel, err := Start(context.Background(), Config{Service: "s"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, done := tel.Start(context.Background(), "connect", call("/x.Y/Z", nil))
	if !trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("with no exporter a call still needs a trace id for its logs")
	}
	done(nil)
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Config{}); err == nil {
		t.Error("Start with no service name")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "grpc")
	if _, err := Start(context.Background(), Config{Service: "s"}); err == nil || !strings.Contains(err.Error(), "only http/protobuf") {
		t.Errorf("grpc protocol = %v", err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	if _, err := Start(context.Background(), Config{Service: "s"}); err == nil || !strings.Contains(err.Error(), "only http/protobuf") {
		t.Errorf("global grpc protocol = %v", err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "zipkin")
	if _, err := Start(context.Background(), Config{Service: "s"}); err == nil || !strings.Contains(err.Error(), "OTEL_TRACES_EXPORTER") {
		t.Errorf("zipkin exporter = %v", err)
	}
}

func TestAPerSignalProtocolOverridesTheGlobalOne(t *testing.T) {
	c := newCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", c.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf")
	tel, err := Start(context.Background(), Config{Service: "s"})
	if err != nil {
		t.Fatal(err)
	}
	tel.Shutdown(context.Background())
}

func TestASignalTurnedOffInTheEnvIsNotExported(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"sdk disabled": {"OTEL_SDK_DISABLED": "true"},
		"none":         {"OTEL_TRACES_EXPORTER": "none", "OTEL_METRICS_EXPORTER": "none"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCollector(t)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", c.URL)
			for k, v := range env {
				t.Setenv(k, v)
			}
			tel, err := Start(context.Background(), Config{Service: "s"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, done := tel.Start(context.Background(), "connect", call("/x.Y/Z", nil))
			if !trace.SpanContextFromContext(ctx).IsValid() {
				t.Error("with export off a call still needs a trace id for its logs")
			}
			done(nil)
			tel.Shutdown(context.Background())
			c.mu.Lock()
			defer c.mu.Unlock()
			if len(c.paths) != 0 {
				t.Fatalf("the collector got %v", c.paths)
			}
		})
	}
}

func TestAnAnonymousCallStartsItsOwnTraceLinkedToTheSentOneWithoutBaggage(t *testing.T) {
	r := newRig(t)
	const sent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"
	h := http.Header{"Traceparent": {sent}, "Baggage": {"tenant=evil"}}
	c := call("/shorten.Rpc/ResolveSlug", h)
	c.Anonymous = true
	outer := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(h))
	ctx, done := r.t.Start(outer, "rest", c)
	if m := baggage.FromContext(ctx).Len(); m != 0 {
		t.Errorf("an anonymous caller planted %d baggage members", m)
	}
	done(nil)
	spans := r.spans.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans recorded; a sampled-off traceparent from the public must not switch sampling off", len(spans))
	}
	s := spans[0]
	if s.SpanContext().TraceID().String() == "0af7651916cd43dd8448eb211c80319c" || s.Parent().IsValid() {
		t.Errorf("the anonymous span joined the trace it was sent: parent %v", s.Parent())
	}
	if l := s.Links(); len(l) != 1 || l[0].SpanContext.TraceID().String() != "0af7651916cd43dd8448eb211c80319c" || l[0].SpanContext.SpanID().String() != "b7ad6b7169203331" {
		t.Errorf("links %v, want one to the sent span", l)
	}
	ctx, done = r.t.Start(context.Background(), "rest", call("/shorten.Rpc/ListLinks", http.Header{"Baggage": {"tenant=acme"}}))
	if v := baggage.FromContext(ctx).Member("tenant").Value(); v != "acme" {
		t.Errorf("a trusted call lost its baggage: %q", v)
	}
	done(nil)
}
