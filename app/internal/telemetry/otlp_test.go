package telemetry_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	colmetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/highclip/two-feet/app/internal/telemetry"
)

// traceSink and metricSink stand in for Dash0's OTLP endpoint.
type traceSink struct {
	coltrace.UnimplementedTraceServiceServer
	mu       sync.Mutex
	requests []*coltrace.ExportTraceServiceRequest
	headers  metadata.MD
}

func (s *traceSink) Export(ctx context.Context, req *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	s.headers, _ = metadata.FromIncomingContext(ctx)
	return &coltrace.ExportTraceServiceResponse{}, nil
}

type metricSink struct {
	colmetric.UnimplementedMetricsServiceServer
	mu       sync.Mutex
	requests []*colmetric.ExportMetricsServiceRequest
}

func (s *metricSink) Export(_ context.Context, req *colmetric.ExportMetricsServiceRequest) (*colmetric.ExportMetricsServiceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	return &colmetric.ExportMetricsServiceResponse{}, nil
}

func stringAttr(attrs []*commonpb.KeyValue, key string) string {
	for _, a := range attrs {
		if a.GetKey() == key {
			return a.GetValue().GetStringValue()
		}
	}
	return ""
}

// TestS10_SetupSendsRedactedSpansAndMetricsToTheOTLPEndpoint runs the production Setup against a
// real gRPC server, configured the way the Helm chart configures it for Dash0.
func TestS10_SetupSendsRedactedSpansAndMetricsToTheOTLPEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	spans, metrics := &traceSink{}, &metricSink{}
	server := grpc.NewServer()
	coltrace.RegisterTraceServiceServer(server, spans)
	colmetric.RegisterMetricsServiceServer(server, metrics)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer%20test-token,Dash0-Dataset=two-feet-dev")
	t.Setenv("OTEL_SERVICE_NAME", "two-feet")

	ctx := context.Background()
	shutdown, err := telemetry.Setup(ctx)
	require.NoError(t, err)

	_, span := telemetry.Start(ctx, "signup")
	span.SetAttributes(attribute.String("post.text", postText), attribute.String("person", email))
	span.End()
	telemetry.RecordSignup(ctx)
	telemetry.RecordPost(ctx, "stuck")
	telemetry.SetSpacesOpen(2)
	require.NoError(t, shutdown(ctx), "shutdown flushes what's buffered")

	spans.mu.Lock()
	defer spans.mu.Unlock()
	require.Len(t, spans.requests, 1)
	resource := spans.requests[0].GetResourceSpans()[0]
	assert.Equal(t, "two-feet", stringAttr(resource.GetResource().GetAttributes(), "service.name"))
	got := resource.GetScopeSpans()[0].GetSpans()[0]
	assert.Equal(t, "signup", got.GetName())
	assert.Equal(t, postText[:40], stringAttr(got.GetAttributes(), "post.text"))
	assert.Equal(t, "[email]", stringAttr(got.GetAttributes(), "person"))
	assert.Equal(t, []string{"Bearer test-token"}, spans.headers.Get("authorization"))
	assert.Equal(t, []string{"two-feet-dev"}, spans.headers.Get("dash0-dataset"))

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	names := map[string]bool{}
	for _, req := range metrics.requests {
		for _, rm := range req.GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					names[m.GetName()] = true
				}
			}
		}
	}
	for _, want := range []string{"signups_total", "posts_total", "spaces_open"} {
		assert.True(t, names[want], "%s reached the endpoint; got %v", want, names)
	}
}
