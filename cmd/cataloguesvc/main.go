package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-kit/kit/log"
	stdopentracing "github.com/opentracing/opentracing-go"
	zipkinot "github.com/openzipkin-contrib/zipkin-go-opentracing"
	"github.com/openzipkin/zipkin-go"
	"github.com/openzipkin/zipkin-go/model"
	zipkinhttp "github.com/openzipkin/zipkin-go/reporter/http"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/microservices-demo/catalogue"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	ServiceName        = "catalogue"
	databaseTarget     = "catalogue-db"
	defaultZipkinHost  = "jaeger-collector.observability.svc.cluster.local"
	defaultZipkinPort  = "9411"
	defaultZipkinURL   = "http://jaeger-collector.observability.svc.cluster.local:9411"
	zipkinSpansAPIPath = "/api/v2/spans"
)

var (
	HTTPLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Time (in seconds) spent serving HTTP requests.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route", "status_code", "ws"})
	HTTPRequestSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_size_bytes",
		Help:    "Size of HTTP request bodies.",
		Buckets: []float64{1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288},
	}, []string{"method", "route"})
	HTTPResponseSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_response_size_bytes",
		Help:    "Size of HTTP response bodies.",
		Buckets: []float64{1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288},
	}, []string{"method", "route"})
	HTTPRequestsInFlight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "http_requests_in_flight",
		Help: "Current number of HTTP requests being served.",
	}, []string{"method", "route"})
)

func init() {
	prometheus.MustRegister(
		HTTPLatency,
		HTTPRequestSize,
		HTTPResponseSize,
		HTTPRequestsInFlight,
	)
}

func main() {
	zipkinCollectorURL := resolveZipkinCollectorURL()
	var (
		port   = flag.String("port", "80", "Port to bind HTTP listener") // TODO(pb): should be -addr, default ":80"
		images = flag.String("images", "./images/", "Image path")
		dsn    = flag.String("DSN", "catalogue_user:default_password@tcp(catalogue-db:3306)/socksdb", "Data Source Name: [username[:password]@][protocol[(address)]]/dbname")
		zip    = flag.String("zipkin", zipkinCollectorURL, "Zipkin collector endpoint")
	)
	flag.Parse()
	*zip = normalizeZipkinCollectorURL(*zip)

	// Mechanical stuff.
	errc := make(chan error)
	ctx := context.Background()

	// Log domain.
	var logger log.Logger
	{
		logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
		logger = log.With(logger, "ts", log.DefaultTimestampUTC)
		logger = log.With(logger, "caller", log.DefaultCaller)
		logger = log.With(logger, "service", ServiceName)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Log(
				"operation", "startup",
				"dependency", "runtime",
				"target", "main",
				"error_type", "panic",
				"error", recovered,
				"stack", string(debug.Stack()),
				"level", "error",
			)
			os.Exit(1)
		}
	}()

	// Tracer domain.
	var tracer stdopentracing.Tracer
	{
		zlogger := log.With(logger, "tracer", "Zipkin")
		zlogger.Log("collector", *zip, "level", "info")

		// Create HTTP reporter
		reporter := zipkinhttp.NewReporter(*zip)

		host := resolveLocalIP(logger)
		hostPort := fmt.Sprintf("%v:%v", host, *port)

		// Convert port string to uint16
		portNum, _ := strconv.Atoi(*port)

		// Create native Zipkin tracer
		nativeTracer, err := zipkin.NewTracer(
			reporter,
			zipkin.WithLocalEndpoint(&model.Endpoint{
				ServiceName: ServiceName,
				IPv4:        net.ParseIP(host),
				Port:        uint16(portNum),
			}),
			// Generate a distinct server span ID instead of sharing the
			// inbound client span ID (Zipkin v2 style).
			zipkin.WithSharedSpans(false),
		)
		if err != nil {
			zlogger.Log(
				"operation", "initialize_tracer",
				"dependency", "zipkin",
				"target", *zip,
				"error_type", classifyDependencyError(err),
				"error", err,
				"level", "error",
			)
			reporter.Close()
			tracer = stdopentracing.NoopTracer{}
		} else {
			// Wrap native Zipkin tracer with OpenTracing bridge
			tracer = zipkinot.Wrap(nativeTracer)
			zlogger.Log("operation", "initialize_tracer", "dependency", "zipkin", "target", hostPort, "msg", "enabled", "level", "info")
		}
		stdopentracing.InitGlobalTracer(tracer)
	}

	traceTags := catalogue.NewTraceTagsFromEnv()

	// Data domain.
	db, err := sqlx.Open("mysql", *dsn)
	if err != nil {
		logger.Log(
			"operation", "open_database",
			"dependency", "mysql",
			"target", databaseTarget,
			"error_type", classifyDependencyError(err),
			"error", err,
			"level", "error",
		)
		os.Exit(1)
	}
	defer db.Close()

	// Optional: log connectivity (do not exit)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		logger.Log(
			"operation", "ping_database",
			"dependency", "mysql",
			"target", databaseTarget,
			"error_type", classifyDependencyError(err),
			"error", err,
			"level", "error",
		)
	}

	// Service domain.
	var service catalogue.Service
	{
		service = catalogue.NewCatalogueService(db, logger)
		service = catalogue.LoggingMiddleware(logger)(service)
	}

	// Endpoint domain.
	endpoints := catalogue.MakeEndpoints(service, tracer, traceTags)

	// HTTP router.
	router := catalogue.MakeHTTPHandler(ctx, endpoints, *images, logger, tracer, traceTags)

	// Handler - keep direct router usage, but add local Prometheus request metrics.
	handler := instrumentHTTPRequests(router, recoverMiddleware(logger, router))

	// Create and launch the HTTP server.
	go func() {
		logger.Log("operation", "listen_http", "dependency", "http_server", "target", ":"+*port, "msg", "serving", "level", "info")
		errc <- http.ListenAndServe(":"+*port, handler)
	}()

	// Capture interrupts.
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errc <- fmt.Errorf("%s", <-c)
	}()

	err = <-errc
	logger.Log(
		"operation", "shutdown",
		"dependency", "runtime",
		"target", "main",
		"error_type", classifyDependencyError(err),
		"error", err,
		"level", shutdownLogLevel(err),
	)
}

func resolveZipkinCollectorURL() string {
	if zipkinURL := os.Getenv("ZIPKIN"); zipkinURL != "" {
		return normalizeZipkinCollectorURL(zipkinURL)
	}

	baseURL := os.Getenv("ZIPKIN_BASE_URL")
	if baseURL == "" {
		host := os.Getenv("ZIPKIN_HOST")
		if host == "" {
			host = defaultZipkinHost
		}
		port := os.Getenv("ZIPKIN_PORT")
		if port == "" {
			port = defaultZipkinPort
		}
		baseURL = "http://" + host + ":" + port
	}
	if baseURL == "" {
		baseURL = defaultZipkinURL
	}
	return normalizeZipkinCollectorURL(baseURL)
}

func normalizeZipkinCollectorURL(raw string) string {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		addr = defaultZipkinURL
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}

	parsed, err := url.Parse(addr)
	if err != nil {
		return addr
	}

	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = zipkinSpansAPIPath
	}
	return parsed.String()
}

func resolveLocalIP(logger log.Logger) string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		localAddr := conn.LocalAddr().(*net.UDPAddr)
		_ = conn.Close()
		return strings.Split(localAddr.String(), ":")[0]
	}

	if podIP := strings.TrimSpace(os.Getenv("POD_IP")); podIP != "" {
		logger.Log(
			"operation", "resolve_local_ip",
			"dependency", "network",
			"target", "8.8.8.8:80",
			"msg", "using POD_IP fallback",
			"pod_ip", podIP,
			"error_type", classifyDependencyError(err),
			"error", err,
			"level", "error",
		)
		return podIP
	}
	if hostIP := strings.TrimSpace(os.Getenv("HOST_IP")); hostIP != "" {
		logger.Log(
			"operation", "resolve_local_ip",
			"dependency", "network",
			"target", "8.8.8.8:80",
			"msg", "using HOST_IP fallback",
			"host_ip", hostIP,
			"error_type", classifyDependencyError(err),
			"error", err,
			"level", "error",
		)
		return hostIP
	}

	logger.Log(
		"operation", "resolve_local_ip",
		"dependency", "network",
		"target", "8.8.8.8:80",
		"msg", "using loopback fallback",
		"error_type", classifyDependencyError(err),
		"error", err,
		"level", "error",
	)
	return "127.0.0.1"
}

func shutdownLogLevel(err error) string {
	if err == nil {
		return "info"
	}
	if errors.Is(err, http.ErrServerClosed) {
		return "info"
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "terminated"), strings.Contains(message, "interrupt"):
		return "info"
	default:
		return "error"
	}
}

func classifyDependencyError(err error) string {
	if err == nil {
		return ""
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "context deadline exceeded"):
		return "timeout"
	case strings.Contains(message, "deadline exceeded"):
		return "timeout"
	case strings.Contains(message, "i/o timeout"):
		return "timeout"
	case strings.Contains(message, "connection refused"):
		return "connection_refused"
	case strings.Contains(message, "no such host"):
		return "dns"
	case strings.Contains(message, "broken pipe"):
		return "connection"
	default:
		return "dependency"
	}
}
