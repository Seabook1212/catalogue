package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

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
	ServiceName = "catalogue"
)

var (
	HTTPLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Time (in seconds) spent serving HTTP requests.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "status_code", "isWS"})
)

func init() {
	prometheus.MustRegister(HTTPLatency)
}

func main() {
	var (
		port   = flag.String("port", "80", "Port to bind HTTP listener") // TODO(pb): should be -addr, default ":80"
		images = flag.String("images", "./images/", "Image path")
		dsn    = flag.String("DSN", "catalogue_user:default_password@tcp(catalogue-db:3306)/socksdb", "Data Source Name: [username[:password]@][protocol[(address)]]/dbname")
		zip    = flag.String("zipkin", os.Getenv("ZIPKIN"), "Zipkin address")
	)
	flag.Parse()

	fmt.Fprintf(os.Stderr, "images: %q\n", *images)
	abs, err := filepath.Abs(*images)
	fmt.Fprintf(os.Stderr, "Abs(images): %q (%v)\n", abs, err)
	pwd, err := os.Getwd()
	fmt.Fprintf(os.Stderr, "Getwd: %q (%v)\n", pwd, err)
	files, _ := filepath.Glob(*images + "/*")
	fmt.Fprintf(os.Stderr, "ls: %q\n", files)

	// Mechanical stuff.
	errc := make(chan error)
	ctx := context.Background()

	// Log domain.
	var logger log.Logger
	{
		logger = log.NewLogfmtLogger(os.Stderr)
		logger = log.With(logger, "ts", log.DefaultTimestampUTC)
		logger = log.With(logger, "caller", log.DefaultCaller)
	}

	// Tracer domain.
	var tracer stdopentracing.Tracer
	{
		if *zip == "" {
			tracer = stdopentracing.NoopTracer{}
			logger.Log("info", "Zipkin tracing disabled - no ZIPKIN env var set")
		} else {
			zlogger := log.With(logger, "tracer", "Zipkin")
			zlogger.Log("addr", *zip)

			// Create HTTP reporter
			reporter := zipkinhttp.NewReporter(*zip)

			// Get local endpoint info
			conn, err := net.Dial("udp", "8.8.8.8:80")
			if err != nil {
				logger.Log("err", err, "msg", "failed to get local IP")
				tracer = stdopentracing.NoopTracer{}
			} else {
				localAddr := conn.LocalAddr().(*net.UDPAddr)
				host := strings.Split(localAddr.String(), ":")[0]
				_ = conn.Close()

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
				)
				if err != nil {
					zlogger.Log("err", err)
					reporter.Close()
					tracer = stdopentracing.NoopTracer{}
				} else {
					// Wrap native Zipkin tracer with OpenTracing bridge
					tracer = zipkinot.Wrap(nativeTracer)
					zlogger.Log("endpoint", hostPort, "msg", "Zipkin tracing enabled")
				}
			}
		}
		stdopentracing.InitGlobalTracer(tracer)
	}

	// Data domain.
	db, err := sqlx.Open("mysql", *dsn)
	if err != nil {
		logger.Log("err", err)
		os.Exit(1)
	}
	defer db.Close()

	// Optional: log connectivity (do not exit)
	if err := db.Ping(); err != nil {
		logger.Log(
			"msg", "Unable to connect to Database",
			"DSN", *dsn,
			"err", err,
		)
	}

	// Service domain.
	var service catalogue.Service
	{
		service = catalogue.NewCatalogueService(db, logger)
		service = catalogue.LoggingMiddleware(logger)(service)
	}

	// Endpoint domain.
	endpoints := catalogue.MakeEndpoints(service, tracer)

	// HTTP router.
	router := catalogue.MakeHTTPHandler(ctx, endpoints, *images, logger, tracer)

	// Handler - use router directly without weaveworks middleware
	// The weaveworks middleware has compatibility issues with the newer versions
	handler := router

	// Create and launch the HTTP server.
	go func() {
		logger.Log("transport", "HTTP", "port", *port)
		errc <- http.ListenAndServe(":"+*port, handler)
	}()

	// Capture interrupts.
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errc <- fmt.Errorf("%s", <-c)
	}()

	logger.Log("exit", <-errc)
}
