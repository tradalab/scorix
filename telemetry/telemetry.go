// Package telemetry exports a scorix service's traces and metrics with OpenTelemetry: a server
// span and a duration for every call through kernel.Observer, the calls the server sheds, and
// the Go runtime's own metrics. Exporters speak OTLP over HTTP and read the standard OTEL_* env.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/observe"
	scorixconnect "github.com/tradalab/scorix/transport/connect"
)

const scope = "github.com/tradalab/scorix/telemetry"

type Config struct {
	Service string
	Version string
}

// Telemetry is a kernel.Observer; pass it as Options.Observer of each Mux, Shed as
// server.Options.OnShed, and Inject in a client's ConnOptions.Header so the callee's span joins.
type Telemetry struct {
	tracer   trace.Tracer
	prop     propagation.TextMapPropagator
	duration metric.Float64Histogram
	active   metric.Int64UpDownCounter
	shed     metric.Int64Counter
	stop     []func(context.Context) error
}

// New instruments with the given providers, for tests or a service that builds its own SDK.
func New(tp trace.TracerProvider, mp metric.MeterProvider) (*Telemetry, error) {
	m := mp.Meter(scope)
	t := &Telemetry{tracer: tp.Tracer(scope), prop: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})}
	var err error
	if t.duration, err = m.Float64Histogram("rpc.server.call.duration", metric.WithUnit("s"),
		metric.WithDescription("How long a call took, from arrival to its last byte."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10)); err != nil {
		return nil, err
	}
	if t.active, err = m.Int64UpDownCounter("scorix.server.active_calls", metric.WithUnit("{call}"),
		metric.WithDescription("Calls being served now.")); err != nil {
		return nil, err
	}
	if t.shed, err = m.Int64Counter("scorix.server.shed_calls", metric.WithUnit("{call}"),
		metric.WithDescription("Calls the server turned away, at server.Options.MaxInFlight or ShedAboveCPU.")); err != nil {
		return nil, err
	}
	return t, nil
}

func exported(signal string) (bool, error) {
	key := "OTEL_EXPORTER_OTLP_" + signal + "_PROTOCOL"
	p := os.Getenv(key)
	if p == "" {
		key, p = "OTEL_EXPORTER_OTLP_PROTOCOL", os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	if p != "" && p != "http/protobuf" {
		return false, fmt.Errorf("telemetry: %s=%s; only http/protobuf is built in", key, p)
	}
	switch e := os.Getenv("OTEL_" + signal + "_EXPORTER"); e {
	case "none":
		return false, nil
	case "", "otlp":
	default:
		return false, fmt.Errorf("telemetry: OTEL_%s_EXPORTER=%s; only otlp and none are built in", signal, e)
	}
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false, nil
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT") != "", nil
}

// Start builds the SDK from the OTEL_* env and sets it as the global provider. A signal with no
// OTLP endpoint, an exporter of none or OTEL_SDK_DISABLED=true is not exported, yet spans still
// get ids so log lines carry trace_id. Only OTLP over http/protobuf is built; the rest is refused.
func Start(ctx context.Context, cfg Config) (*Telemetry, error) {
	if cfg.Service == "" {
		return nil, errors.New("telemetry: Config.Service is required")
	}
	traces, err := exported("TRACES")
	if err != nil {
		return nil, err
	}
	metrics, err := exported("METRICS")
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithProcessPID(), resource.WithProcessRuntimeName(), resource.WithProcessRuntimeVersion(),
		resource.WithAttributes(attribute.String("service.name", cfg.Service), attribute.String("service.version", cfg.Version)))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	topts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if traces {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("telemetry: traces: %w", err)
		}
		topts = append(topts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(topts...)
	mopts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if metrics {
		exp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			tp.Shutdown(ctx)
			return nil, fmt.Errorf("telemetry: metrics: %w", err)
		}
		mopts = append(mopts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
	}
	mp := sdkmetric.NewMeterProvider(mopts...)
	t, err := New(tp, mp)
	if err == nil {
		err = runtime.Start(runtime.WithMeterProvider(mp))
	}
	if err != nil {
		tp.Shutdown(ctx)
		mp.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: metrics: %w", err)
	}
	t.stop = []func(context.Context) error{tp.Shutdown, mp.Shutdown}
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(t.prop)
	return t, nil
}

// Shutdown flushes what is buffered and stops the exporters; call it once the server has stopped.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, stop := range t.stop {
		errs = append(errs, stop(ctx))
	}
	return errors.Join(errs...)
}

var systems = map[string]string{"connect": "connect_rpc"}

func split(procedure string) (service, method string) {
	service, method, _ = strings.Cut(strings.TrimPrefix(procedure, "/"), "/")
	return service, method
}

var serverFaults = map[connect.Code]bool{
	connect.CodeUnknown: true, connect.CodeDeadlineExceeded: true, connect.CodeUnimplemented: true,
	connect.CodeInternal: true, connect.CodeUnavailable: true, connect.CodeDataLoss: true,
}

// Start opens the server span of one call, as the child of a traceparent the caller sent. An
// Anonymous call is untrusted: its span is a new root linked to that traceparent, without baggage.
func (t *Telemetry) Start(ctx context.Context, transport string, call kernel.Call) (context.Context, func(error)) {
	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer)}
	if call.Anonymous {
		sent := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(call.Header)))
		opts = append(opts, trace.WithNewRoot(), trace.WithLinks(trace.Link{SpanContext: sent}))
	} else {
		ctx = t.prop.Extract(ctx, propagation.HeaderCarrier(call.Header))
	}
	service, method := split(call.Procedure)
	system := systems[transport]
	if system == "" {
		system = transport
	}
	attrs := []attribute.KeyValue{attribute.String("rpc.system", system), attribute.String("rpc.service", service), attribute.String("rpc.method", method)}
	ctx, span := t.tracer.Start(ctx, strings.TrimPrefix(call.Procedure, "/"), append(opts, trace.WithAttributes(attrs...))...)
	if sc := span.SpanContext(); sc.IsValid() {
		ctx = observe.With(ctx, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	}
	set := metric.WithAttributes(attrs...)
	t.active.Add(ctx, 1, set)
	start := time.Now()
	return ctx, func(err error) {
		code := "ok"
		if err != nil {
			c := scorixconnect.Code(err)
			code = c.String()
			if serverFaults[c] {
				span.SetStatus(codes.Error, code)
			}
		}
		status := attribute.String("rpc.response.status_code", code)
		span.SetAttributes(status)
		t.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(append(attrs, status)...))
		t.active.Add(ctx, -1, set)
		span.End()
	}
}

// Shed counts a call server.Options.MaxInFlight or ShedAboveCPU turned away.
func (t *Telemetry) Shed(r *http.Request) {
	t.shed.Add(r.Context(), 1)
}

// Inject writes ctx's trace into h, as a ConnOptions.Header (or part of one) for outgoing calls.
func (t *Telemetry) Inject(ctx context.Context, h http.Header) {
	t.prop.Inject(ctx, propagation.HeaderCarrier(h))
}

var _ kernel.Observer = (*Telemetry)(nil)
