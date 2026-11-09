package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// NewPool connects to Postgres with every query traced.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.ConnConfig.Tracer = postgresTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

// postgresTracer records the parameterized SQL and never the arguments, which hold user text. It
// only joins a trace already in progress, otherwise every probe and background query would be a
// trace of its own.
type postgresTracer struct{}

func (postgresTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	operation := sqlOperation(data.SQL)
	ctx, _ = otel.Tracer(instrumentationName).Start(ctx, "postgres "+operation,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system.name", "postgresql"),
			attribute.String("db.operation.name", operation),
			attribute.String("db.query.text", data.SQL),
		),
	)
	return ctx
}

func (postgresTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	defer span.End()
	if data.Err == nil || errors.Is(data.Err, pgx.ErrNoRows) {
		return
	}
	// Only the error's class: Postgres messages can quote the values in the row.
	span.SetStatus(codes.Error, "query failed")
	var pgErr *pgconn.PgError
	if errors.As(data.Err, &pgErr) {
		span.SetAttributes(attribute.String("db.response.status_code", pgErr.Code))
	}
}

// sqlOperation picks a span name from a closed list.
func sqlOperation(sql string) string {
	words := strings.Fields(sql)
	if len(words) == 0 {
		return "QUERY"
	}
	switch verb := strings.ToUpper(words[0]); verb {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "WITH", "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE":
		return verb
	default:
		return "QUERY"
	}
}
