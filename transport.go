package catalogue

// transport.go contains the binding from endpoints to a concrete transport.
// In our case we just use a REST-y HTTP transport.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-kit/kit/circuitbreaker"
	"github.com/go-kit/kit/log"
	kittransport "github.com/go-kit/kit/transport"
	httptransport "github.com/go-kit/kit/transport/http"
	"github.com/gorilla/mux"
	stdopentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sony/gobreaker"
)

type requestContextKey string

const requestMetadataKey requestContextKey = "request-metadata"

type requestMetadata struct {
	method    string
	target    string
	startedAt time.Time
}

// MakeHTTPHandler mounts the endpoints into a REST-y HTTP handler.
func MakeHTTPHandler(ctx context.Context, e Endpoints, imagePath string, logger log.Logger, tracer stdopentracing.Tracer, traceTags TraceTags) *mux.Router {
	r := mux.NewRouter().StrictSlash(false)

	options := []httptransport.ServerOption{
		httptransport.ServerBefore(captureRequestMetadata()),
		httptransport.ServerErrorHandler(newTransportErrorHandler(logger)),
		httptransport.ServerErrorEncoder(encodeError),
		httptransport.ServerBefore(extractTracingContext(tracer, traceTags)),
		httptransport.ServerFinalizer(finalizeTracingSpan()),
	}

	// GET /catalogue       List
	// GET /catalogue/size  Count
	// GET /catalogue/{id}  Get
	// GET /tags            Tags
	// GET /health          Health Check

	r.Methods("GET").Path("/catalogue").Handler(httptransport.NewServer(
		circuitbreaker.Gobreaker(gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:    "List",
			Timeout: 30 * time.Second,
		}))(e.ListEndpoint),
		decodeListRequest,
		encodeListResponse,
		options...,
	))

	r.Methods("GET").Path("/catalogue/size").Handler(httptransport.NewServer(
		circuitbreaker.Gobreaker(gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:    "Count",
			Timeout: 30 * time.Second,
		}))(e.CountEndpoint),
		decodeCountRequest,
		encodeResponse,
		options...,
	))

	r.Methods("GET").Path("/catalogue/{id}").Handler(httptransport.NewServer(
		circuitbreaker.Gobreaker(gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:    "Get",
			Timeout: 30 * time.Second,
		}))(e.GetEndpoint),
		decodeGetRequest,
		encodeGetResponse, // special case, this one can have an error
		options...,
	))

	r.Methods("GET").Path("/tags").Handler(httptransport.NewServer(
		circuitbreaker.Gobreaker(gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:    "Tags",
			Timeout: 30 * time.Second,
		}))(e.TagsEndpoint),
		decodeTagsRequest,
		encodeResponse,
		options...,
	))

	// Wrap file server with tracing middleware
	fileServer := http.StripPrefix("/catalogue/images/", http.FileServer(http.Dir(imagePath)))
	r.Methods("GET").PathPrefix("/catalogue/images/").Handler(
		wrapHandlerWithTracing(tracer, traceTags, fileServer),
	)

	r.Methods("GET").PathPrefix("/health").Handler(httptransport.NewServer(
		circuitbreaker.Gobreaker(gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:    "Health",
			Timeout: 30 * time.Second,
		}))(e.HealthEndpoint),
		decodeHealthRequest,
		encodeHealthResponse,
		options...,
	))

	r.Handle("/metrics", promhttp.Handler())
	return r
}

func encodeError(_ context.Context, err error, w http.ResponseWriter) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	}

	// 先写 Header 再 WriteHeader 更标准
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":       err.Error(),
		"status_code": code,
		"status_text": http.StatusText(code),
	})
}

func decodeListRequest(_ context.Context, r *http.Request) (interface{}, error) {
	pageNum := 1
	if page := r.FormValue("page"); page != "" {
		pageNum, _ = strconv.Atoi(page)
	}

	pageSize := 10
	if size := r.FormValue("size"); size != "" {
		pageSize, _ = strconv.Atoi(size)
	}

	order := "id"
	if sort := r.FormValue("sort"); sort != "" {
		order = strings.ToLower(sort)
	}

	tags := []string{}
	if tagsval := r.FormValue("tags"); tagsval != "" {
		tags = strings.Split(tagsval, ",")
	}

	return listRequest{
		Tags:     tags,
		Order:    order,
		PageNum:  pageNum,
		PageSize: pageSize,
	}, nil
}

// encodeListResponse is distinct from the generic encodeResponse because our
// clients expect that we will encode the slice (array) of socks directly,
// without the wrapping response object.
func encodeListResponse(ctx context.Context, w http.ResponseWriter, response interface{}) error {
	resp := response.(listResponse)
	return encodeResponse(ctx, w, resp.Socks)
}

func decodeCountRequest(_ context.Context, r *http.Request) (interface{}, error) {
	tags := []string{}
	if tagsval := r.FormValue("tags"); tagsval != "" {
		tags = strings.Split(tagsval, ",")
	}
	return countRequest{Tags: tags}, nil
}

func decodeGetRequest(_ context.Context, r *http.Request) (interface{}, error) {
	return getRequest{
		ID: mux.Vars(r)["id"],
	}, nil
}

// encodeGetResponse is distinct from the generic encodeResponse because we need
// to special-case when the getResponse object contains a non-nil error.
func encodeGetResponse(ctx context.Context, w http.ResponseWriter, response interface{}) error {
	resp := response.(getResponse)
	if resp.Err != nil {
		encodeError(ctx, resp.Err, w)
		return nil
	}
	return encodeResponse(ctx, w, resp.Sock)
}

func decodeTagsRequest(_ context.Context, r *http.Request) (interface{}, error) {
	return struct{}{}, nil
}

func decodeHealthRequest(_ context.Context, r *http.Request) (interface{}, error) {
	return struct{}{}, nil
}

func encodeHealthResponse(ctx context.Context, w http.ResponseWriter, response interface{}) error {
	return encodeResponse(ctx, w, response.(healthResponse))
}

func encodeResponse(_ context.Context, w http.ResponseWriter, response interface{}) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(w).Encode(response)
}

func captureRequestMetadata() httptransport.RequestFunc {
	return func(ctx context.Context, r *http.Request) context.Context {
		return context.WithValue(ctx, requestMetadataKey, requestMetadata{
			method:    r.Method,
			target:    r.URL.Path,
			startedAt: time.Now(),
		})
	}
}

func requestMetadataFromContext(ctx context.Context) requestMetadata {
	meta, ok := ctx.Value(requestMetadataKey).(requestMetadata)
	if !ok {
		return requestMetadata{}
	}
	return meta
}

func newTransportErrorHandler(logger log.Logger) kittransport.ErrorHandler {
	return kittransport.ErrorHandlerFunc(func(ctx context.Context, err error) {
		if err == nil {
			return
		}

		if errors.Is(err, ErrNotFound) {
			return
		}

		AnnotateSpanError(stdopentracing.SpanFromContext(ctx), classifyTransportError(err), err)
		if errors.Is(err, ErrDBConnection) {
			return
		}

		meta := requestMetadataFromContext(ctx)
		args := append(TraceFieldsFromContext(ctx),
			"service", catalogueServiceName,
			"operation", meta.method,
			"dependency", transportDependency(err),
			"target", meta.target,
			"error_type", classifyTransportError(err),
			"error", err,
			"latency_ms", time.Since(meta.startedAt).Milliseconds(),
			"level", "error",
		)
		_ = logger.Log(args...)
	})
}

func classifyTransportError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrDBConnection):
		return "database"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, gobreaker.ErrOpenState), errors.Is(err, gobreaker.ErrTooManyRequests):
		return "circuit_open"
	default:
		return classifyDependencyError(err)
	}
}

func transportDependency(err error) string {
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return "circuit_breaker"
	}
	return "http_server"
}

// wrapHandlerWithTracing wraps a standard http.Handler with OpenTracing support
func wrapHandlerWithTracing(tracer stdopentracing.Tracer, traceTags TraceTags, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract the span context from HTTP headers
		spanContext, err := tracer.Extract(
			stdopentracing.HTTPHeaders,
			stdopentracing.HTTPHeadersCarrier(r.Header),
		)

		var span stdopentracing.Span
		if err != nil || spanContext == nil {
			// No parent span, create a new root span
			span = tracer.StartSpan(
				r.Method+" "+r.URL.Path,
				ext.SpanKindRPCServer,
			)
		} else {
			// Create a child span
			span = tracer.StartSpan(
				r.Method+" "+r.URL.Path,
				stdopentracing.ChildOf(spanContext),
				ext.SpanKindRPCServer,
			)
		}
		span.SetTag("span.kind", string(ext.SpanKindRPCServerEnum))
		span.SetTag("http.method", r.Method)
		span.SetTag("http.url", r.URL.RequestURI())
		traceTags.apply(span)
		defer span.Finish()

		rw := newStatusRecordingResponseWriter(w)

		// Add span to context
		ctx := stdopentracing.ContextWithSpan(r.Context(), span)
		r = r.WithContext(ctx)

		// Call the actual handler
		handler.ServeHTTP(rw, r)
		span.SetTag("http.status_code", rw.StatusCode())
	})
}

// extractTracingContext extracts OpenTraacing span context from HTTP headers
func extractTracingContext(tracer stdopentracing.Tracer, traceTags TraceTags) httptransport.RequestFunc {
	return func(ctx context.Context, r *http.Request) context.Context {
		// Skip tracing for health and metrics endpoints to reduce noise
		if strings.HasPrefix(r.URL.Path, "/health") || strings.HasPrefix(r.URL.Path, "/metrics") {
			return ctx
		}

		// Extract the span context from HTTP headers
		spanContext, err := tracer.Extract(
			stdopentracing.HTTPHeaders,
			stdopentracing.HTTPHeadersCarrier(r.Header),
		)

		startOptions := []stdopentracing.StartSpanOption{ext.SpanKindRPCServer}
		if err == nil && spanContext != nil {
			startOptions = append(startOptions, stdopentracing.ChildOf(spanContext))
		}

		span := tracer.StartSpan(r.Method+" "+r.URL.Path, startOptions...)
		span.SetTag("span.kind", string(ext.SpanKindRPCServerEnum))
		span.SetTag("http.method", r.Method)
		span.SetTag("http.url", r.URL.RequestURI())
		traceTags.apply(span)
		// The span will be finished by the HTTP server finalizer.
		// Store it in context for the endpoint to use
		ctx = stdopentracing.ContextWithSpan(ctx, span)

		return ctx
	}
}

func finalizeTracingSpan() httptransport.ServerFinalizerFunc {
	return func(ctx context.Context, code int, r *http.Request) {
		span := stdopentracing.SpanFromContext(ctx)
		if span == nil {
			return
		}

		span.SetTag("http.method", r.Method)
		span.SetTag("http.status_code", code)
		if code >= http.StatusInternalServerError {
			ext.Error.Set(span, true)
		}
		span.Finish()
	}
}

type statusRecordingResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func newStatusRecordingResponseWriter(w http.ResponseWriter) *statusRecordingResponseWriter {
	return &statusRecordingResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}
}

func (w *statusRecordingResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecordingResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecordingResponseWriter) StatusCode() int {
	return w.statusCode
}
