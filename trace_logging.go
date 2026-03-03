package catalogue

import (
	"context"
	"fmt"

	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	zipkinot "github.com/openzipkin-contrib/zipkin-go-opentracing"
)

func TraceFieldsFromContext(ctx context.Context) []interface{} {
	return TraceFieldsFromSpan(opentracing.SpanFromContext(ctx))
}

func TraceFieldsFromSpan(span opentracing.Span) []interface{} {
	if span == nil {
		return nil
	}

	spanContext := span.Context()
	if zipkinSpanContext, ok := spanContext.(zipkinot.SpanContext); ok {
		return []interface{}{
			"traceid", fmt.Sprintf("%016x", zipkinSpanContext.TraceID.Low),
			"spanid", fmt.Sprintf("%016x", uint64(zipkinSpanContext.ID)),
		}
	}

	return []interface{}{
		"traceid", fmt.Sprintf("%v", spanContext),
	}
}

func AnnotateSpanError(span opentracing.Span, errorType string, err error) {
	if span == nil || err == nil {
		return
	}

	ext.Error.Set(span, true)
	if errorType != "" {
		span.SetTag("error.type", errorType)
	}
	span.SetTag("error.message", err.Error())
}
