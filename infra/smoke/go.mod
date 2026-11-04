module github.com/highclip/two-feet/infra/smoke

go 1.24

require (
	github.com/clerk/clerk-sdk-go/v2 v2.3.1
	github.com/jackc/pgx/v5 v5.7.5
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.60.0
	go.opentelemetry.io/otel v1.35.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.35.0
	go.opentelemetry.io/otel/sdk v1.35.0
)
