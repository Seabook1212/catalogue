package catalogue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/opentracing/opentracing-go"
	zipkinot "github.com/openzipkin-contrib/zipkin-go-opentracing"
)

// LoggingMiddleware logs method calls, parameters, results, and elapsed time.
func LoggingMiddleware(logger log.Logger) Middleware {
	return func(next Service) Service {
		return loggingMiddleware{
			next:   next,
			logger: logger,
		}
	}
}

type loggingMiddleware struct {
	next   Service
	logger log.Logger
}

// extractTraceInfo extracts trace ID and span ID from context
func extractTraceInfo(ctx context.Context) (traceID, spanID string) {
	span := opentracing.SpanFromContext(ctx)
	if span == nil {
		return "", ""
	}

	spanContext := span.Context()

	// Try to extract Zipkin span context
	if zipkinSpanContext, ok := spanContext.(zipkinot.SpanContext); ok {
		// Get the native Zipkin span context
		// For Zipkin, use only the Low part of TraceID (64-bit) to match Jaeger format
		// SpanID is also a uint64 value
		traceID := fmt.Sprintf("%016x", zipkinSpanContext.TraceID.Low)
		spanID := fmt.Sprintf("%016x", uint64(zipkinSpanContext.ID))
		return traceID, spanID
	}

	// Fallback: try string conversion
	traceIDStr := fmt.Sprintf("%v", spanContext)
	return traceIDStr, ""
}

// logWithTrace adds trace context to logger
func (mw loggingMiddleware) logWithTrace(ctx context.Context, keyvals ...interface{}) {
	traceID, spanID := extractTraceInfo(ctx)

	// Build log args with trace context
	args := make([]interface{}, 0, len(keyvals)+4)
	if traceID != "" {
		args = append(args, "traceid", traceID)
	}
	if spanID != "" {
		args = append(args, "spanid", spanID)
	}
	args = append(args, keyvals...)

	mw.logger.Log(args...)
}

func (mw loggingMiddleware) List(ctx context.Context, tags []string, order string, pageNum, pageSize int) (socks []Sock, err error) {
	defer func(begin time.Time) {
		mw.logWithTrace(ctx,
			"method", "List",
			"tags", strings.Join(tags, ", "),
			"order", order,
			"pageNum", pageNum,
			"pageSize", pageSize,
			"result", len(socks),
			"err", err,
			"took", time.Since(begin),
		)
	}(time.Now())
	return mw.next.List(ctx, tags, order, pageNum, pageSize)
}

func (mw loggingMiddleware) Count(ctx context.Context, tags []string) (n int, err error) {
	defer func(begin time.Time) {
		mw.logWithTrace(ctx,
			"method", "Count",
			"tags", strings.Join(tags, ", "),
			"result", n,
			"err", err,
			"took", time.Since(begin),
		)
	}(time.Now())
	return mw.next.Count(ctx, tags)
}

func (mw loggingMiddleware) Get(ctx context.Context, id string) (s Sock, err error) {
	defer func(begin time.Time) {
		mw.logWithTrace(ctx,
			"method", "Get",
			"id", id,
			"sock", s.ID,
			"err", err,
			"took", time.Since(begin),
		)
	}(time.Now())
	return mw.next.Get(ctx, id)
}

func (mw loggingMiddleware) Tags(ctx context.Context) (tags []string, err error) {
	defer func(begin time.Time) {
		mw.logWithTrace(ctx,
			"method", "Tags",
			"result", len(tags),
			"err", err,
			"took", time.Since(begin),
		)
	}(time.Now())
	return mw.next.Tags(ctx)
}

func (mw loggingMiddleware) Health(ctx context.Context) (health []Health) {
	// defer func(begin time.Time) {
	// 	mw.logger.Log(
	// 		"method", "Health",
	// 		"result", len(health),
	// 		"took", time.Since(begin),
	// 	)
	// }(time.Now())
	return mw.next.Health(ctx)
}
