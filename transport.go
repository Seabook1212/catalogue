package catalogue

// transport.go contains the binding from endpoints to a concrete transport.
// In our case we just use a REST-y HTTP transport.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-kit/kit/circuitbreaker"
	"github.com/go-kit/kit/log"
	httptransport "github.com/go-kit/kit/transport/http"
	"github.com/gorilla/mux"
	stdopentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sony/gobreaker"
)

// MakeHTTPHandler mounts the endpoints into a REST-y HTTP handler.
func MakeHTTPHandler(ctx context.Context, e Endpoints, imagePath string, logger log.Logger, tracer stdopentracing.Tracer) *mux.Router {
	r := mux.NewRouter().StrictSlash(false)

	options := []httptransport.ServerOption{
		httptransport.ServerErrorLogger(logger),
		httptransport.ServerErrorEncoder(encodeError),
		httptransport.ServerBefore(extractTracingContext(tracer)),
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

	r.Methods("GET").PathPrefix("/catalogue/images/").Handler(http.StripPrefix(
		"/catalogue/images/",
		http.FileServer(http.Dir(imagePath)),
	))

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
	switch err {
	case ErrNotFound:
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

// extractTracingContext extracts OpenTracing span context from HTTP headers
func extractTracingContext(tracer stdopentracing.Tracer) httptransport.RequestFunc {
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

		if err != nil && err != stdopentracing.ErrSpanContextNotFound {
			// Log error but continue - don't break the request
			return ctx
		}

		// If we found a span context, create a new span as a child of it
		if spanContext != nil {
			span := tracer.StartSpan(
				r.Method+" "+r.URL.Path,
				stdopentracing.ChildOf(spanContext),
				ext.SpanKindRPCServer,
			)
			// The span will be finished by the endpoint middleware
			// Store it in context for the endpoint to use
			ctx = stdopentracing.ContextWithSpan(ctx, span)
		}

		return ctx
	}
}
