package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/gorilla/mux"
	"github.com/microservices-demo/catalogue"
	opentracing "github.com/opentracing/opentracing-go"
)

type statusRecordingResponseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
	wroteHeader  bool
}

func (w *statusRecordingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *statusRecordingResponseWriter) Write(b []byte) (int, error) {
	// Default to 200 when handlers don't call WriteHeader explicitly.
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	w.wroteHeader = true
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}

func (w *statusRecordingResponseWriter) HeaderWritten() bool {
	return w.wroteHeader
}

func instrumentHTTPRequests(router *mux.Router, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		route := routeLabelForRequest(router, r)
		method := r.Method
		ws := strconv.FormatBool(isWebSocketRequest(r))

		inFlight := HTTPRequestsInFlight.WithLabelValues(method, route)
		inFlight.Inc()
		defer inFlight.Dec()

		rw := &statusRecordingResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		next.ServeHTTP(rw, r)

		HTTPLatency.WithLabelValues(
			method,
			route,
			strconv.Itoa(rw.statusCode),
			ws,
		).Observe(time.Since(start).Seconds())
		HTTPRequestSize.WithLabelValues(method, route).Observe(float64(requestSizeBytes(r)))
		HTTPResponseSize.WithLabelValues(method, route).Observe(float64(rw.bytesWritten))
	})
}

func routeLabelForRequest(router *mux.Router, r *http.Request) string {
	var match mux.RouteMatch
	if router != nil && router.Match(r, &match) && match.Route != nil {
		path, err := match.Route.GetPathTemplate()
		if err == nil && path != "" {
			return normalizeRouteLabel(path)
		}
		if name := match.Route.GetName(); name != "" {
			return normalizeRouteLabel(name)
		}
	}

	return normalizeRouteLabel(r.URL.Path)
}

func normalizeRouteLabel(path string) string {
	label := strings.TrimPrefix(path, "/")
	if label == "" {
		return "root"
	}
	return label
}

func requestSizeBytes(r *http.Request) int {
	if r.ContentLength > 0 {
		return int(r.ContentLength)
	}
	return 0
}

func isWebSocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func recoverMiddleware(logger log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		begin := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				catalogue.AnnotateSpanError(opentracing.SpanFromContext(r.Context()), "panic", fmt.Errorf("%v", recovered))
				args := append(catalogue.TraceFieldsFromContext(r.Context()),
					"service", ServiceName,
					"operation", r.Method,
					"dependency", "http_server",
					"target", r.URL.Path,
					"error_type", "panic",
					"error", recovered,
					"latency_ms", time.Since(begin).Milliseconds(),
					"stack", string(debug.Stack()),
					"level", "error",
				)
				_ = logger.Log(args...)

				if writer, ok := w.(interface{ HeaderWritten() bool }); !ok || !writer.HeaderWritten() {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"error":       http.StatusText(http.StatusInternalServerError),
						"status_code": http.StatusInternalServerError,
						"status_text": http.StatusText(http.StatusInternalServerError),
					})
				}
			}
		}()

		next.ServeHTTP(w, r)
	})
}
