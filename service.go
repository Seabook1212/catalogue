package catalogue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
)

type Service interface {
	List(ctx context.Context, tags []string, order string, pageNum, pageSize int) ([]Sock, error)
	Count(ctx context.Context, tags []string) (int, error)
	Get(ctx context.Context, id string) (Sock, error)
	Tags(ctx context.Context) ([]string, error)
	Health(ctx context.Context) []Health
}

type Middleware func(Service) Service

type Sock struct {
	ID          string   `json:"id" db:"id"`
	Name        string   `json:"name" db:"name"`
	Description string   `json:"description" db:"description"`
	ImageURL    []string `json:"imageUrl" db:"-"`
	ImageURL_1  string   `json:"-" db:"image_url_1"`
	ImageURL_2  string   `json:"-" db:"image_url_2"`
	Price       float32  `json:"price" db:"price"`
	Count       int      `json:"count" db:"count"`
	Tags        []string `json:"tag" db:"-"`
	TagString   string   `json:"-" db:"tag_name"`
}

type Health struct {
	Service string `json:"service"`
	Status  string `json:"status"`
	Time    string `json:"time"`
}

var ErrNotFound = errors.New("not found")
var ErrDBConnection = errors.New("database connection error")

var baseQuery = "SELECT sock.sock_id AS id, sock.name, sock.description, sock.price, sock.count, sock.image_url_1, sock.image_url_2, GROUP_CONCAT(tag.name) AS tag_name FROM sock JOIN sock_tag ON sock.sock_id=sock_tag.sock_id JOIN tag ON sock_tag.tag_id=tag.tag_id"

const catalogueServiceName = "catalogue"

func NewCatalogueService(db *sqlx.DB, logger log.Logger) Service {
	return &catalogueService{
		db:        db,
		logger:    logger,
		traceTags: NewTraceTagsFromEnv(),
	}
}

type catalogueService struct {
	db        *sqlx.DB
	logger    log.Logger
	traceTags TraceTags
}

const defaultDBPeerService = "catalogue-db"

func startOutboundSpan(ctx context.Context, operation, peerService string, traceTags TraceTags) opentracing.Span {
	tags := opentracing.Tags{
		string(ext.SpanKind): ext.SpanKindRPCClientEnum,
	}
	if peerService != "" {
		tags[string(ext.PeerService)] = peerService
	}

	options := []opentracing.StartSpanOption{tags}
	if parentSpan := opentracing.SpanFromContext(ctx); parentSpan != nil {
		options = append(options, opentracing.ChildOf(parentSpan.Context()))
	}

	span := opentracing.StartSpan(operation, options...)
	traceTags.apply(span)
	return span
}

func startDBSpan(ctx context.Context, operation, statement, peerService string, traceTags TraceTags) opentracing.Span {
	if peerService == "" {
		peerService = defaultDBPeerService
	}

	span := startOutboundSpan(ctx, operation, peerService, traceTags)
	span.SetTag("db.type", "mysql")
	if statement != "" {
		span.SetTag("db.statement", statement)
	}
	return span
}

type boundaryError struct {
	public error
	cause  error
}

func (e *boundaryError) Error() string {
	return e.public.Error()
}

func (e *boundaryError) Unwrap() error {
	if e.cause == nil {
		return e.public
	}
	return errors.Join(e.public, e.cause)
}

func wrapBoundaryError(public error, message string, err error) error {
	if err == nil {
		return public
	}
	return &boundaryError{
		public: public,
		cause:  fmt.Errorf("%s: %w", message, err),
	}
}

func classifyDependencyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, sql.ErrNoRows):
		return "not_found"
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "connection"
	}

	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1040:
			return "pool_exhausted"
		case 1205:
			return "timeout"
		case 1213:
			return "deadlock"
		}
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "too many connections"):
		return "pool_exhausted"
	case strings.Contains(message, "connection refused"):
		return "connection_refused"
	case strings.Contains(message, "no such host"):
		return "dns"
	case strings.Contains(message, "deadline exceeded"):
		return "timeout"
	case strings.Contains(message, "i/o timeout"):
		return "timeout"
	case strings.Contains(message, "deadlock"):
		return "deadlock"
	case strings.Contains(message, "rollback"):
		return "rollback"
	case strings.Contains(message, "driver: bad connection"):
		return "connection"
	case strings.Contains(message, "invalid connection"):
		return "connection"
	case strings.Contains(message, "broken pipe"):
		return "connection"
	case strings.Contains(message, "connection reset"):
		return "connection"
	default:
		return "database"
	}
}

func logDBFailure(logger log.Logger, span opentracing.Span, operation, target string, begin time.Time, err error) {
	errorType := classifyDependencyError(err)
	if span != nil {
		AnnotateSpanError(span, errorType, err)
		span.LogKV("event", "error", "error_type", errorType, "message", err.Error())
	}

	args := append(TraceFieldsFromSpan(span),
		"service", catalogueServiceName,
		"operation", operation,
		"dependency", "mysql",
		"target", target,
		"error_type", errorType,
		"error", err,
		"latency_ms", time.Since(begin).Milliseconds(),
		"level", "error",
	)
	_ = logger.Log(args...)
}

func (s *catalogueService) List(ctx context.Context, tags []string, order string, pageNum, pageSize int) ([]Sock, error) {
	begin := time.Now()
	var socks []Sock
	query := baseQuery

	var args []interface{}
	for i, t := range tags {
		if i == 0 {
			query += " WHERE tag.name=?"
			args = append(args, t)
		} else {
			query += " OR tag.name=?"
			args = append(args, t)
		}
	}

	query += " GROUP BY id"
	if order != "" {
		// 原逻辑保持不动（注意：ORDER BY ? 在 MySQL 里通常不按列名工作）
		query += " ORDER BY ?"
		args = append(args, order)
	}
	query += ";"

	span := startDBSpan(ctx, "mysql SELECT catalogue list", query, defaultDBPeerService, s.traceTags)
	defer span.Finish()

	err := s.db.SelectContext(ctx, &socks, query, args...)
	if err != nil {
		logDBFailure(s.logger, span, "select", "sock", begin, err)
		return []Sock{}, wrapBoundaryError(ErrDBConnection, "select catalogue list", err)
	}

	for i := range socks {
		socks[i].ImageURL = []string{socks[i].ImageURL_1, socks[i].ImageURL_2}
		socks[i].Tags = strings.Split(socks[i].TagString, ",")
	}

	time.Sleep(0 * time.Millisecond)
	socks = cut(socks, pageNum, pageSize)
	return socks, nil
}

func (s *catalogueService) Count(ctx context.Context, tags []string) (int, error) {
	begin := time.Now()
	query := "SELECT COUNT(DISTINCT sock.sock_id) FROM sock JOIN sock_tag ON sock.sock_id=sock_tag.sock_id JOIN tag ON sock_tag.tag_id=tag.tag_id"
	var args []interface{}

	for i, t := range tags {
		if i == 0 {
			query += " WHERE tag.name=?"
			args = append(args, t)
		} else {
			query += " OR tag.name=?"
			args = append(args, t)
		}
	}
	query += ";"

	span := startDBSpan(ctx, "mysql SELECT catalogue count", query, defaultDBPeerService, s.traceTags)
	defer span.Finish()

	var count int
	if err := s.db.QueryRowxContext(ctx, query, args...).Scan(&count); err != nil {
		logDBFailure(s.logger, span, "select", "sock", begin, err)
		return 0, wrapBoundaryError(ErrDBConnection, "count catalogue items", err)
	}

	return count, nil
}

func (s *catalogueService) Get(ctx context.Context, id string) (Sock, error) {
	begin := time.Now()
	query := baseQuery + " WHERE sock.sock_id =? GROUP BY sock.sock_id;"

	span := startDBSpan(ctx, "mysql SELECT catalogue get", query, defaultDBPeerService, s.traceTags)
	span.SetTag("db.id", id)
	defer span.Finish()

	var sock Sock
	err := s.db.GetContext(ctx, &sock, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Sock{}, wrapBoundaryError(ErrNotFound, "get catalogue item", err)
		}
		logDBFailure(s.logger, span, "select", "sock", begin, err)
		return Sock{}, wrapBoundaryError(ErrDBConnection, "get catalogue item", err)
	}

	sock.ImageURL = []string{sock.ImageURL_1, sock.ImageURL_2}
	sock.Tags = strings.Split(sock.TagString, ",")

	return sock, nil
}

func (s *catalogueService) Tags(ctx context.Context) ([]string, error) {
	begin := time.Now()
	query := "SELECT name FROM tag;"

	span := startDBSpan(ctx, "mysql SELECT catalogue tags", query, defaultDBPeerService, s.traceTags)
	defer span.Finish()

	rows, err := s.db.QueryxContext(ctx, query)
	if err != nil {
		logDBFailure(s.logger, span, "select", "tag", begin, err)
		return []string{}, wrapBoundaryError(ErrDBConnection, "list catalogue tags", err)
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			logDBFailure(s.logger, span, "select", "tag", begin, err)
			return []string{}, wrapBoundaryError(ErrDBConnection, "scan catalogue tags", err)
		}
		tags = append(tags, tag)
	}

	if err := rows.Err(); err != nil {
		logDBFailure(s.logger, span, "select", "tag", begin, err)
		return []string{}, wrapBoundaryError(ErrDBConnection, "iterate catalogue tags", err)
	}

	return tags, nil
}

func (s *catalogueService) Health(ctx context.Context) []Health {
	var health []Health
	dbstatus := "OK"

	if err := s.db.PingContext(ctx); err != nil {
		dbstatus = "err"
	}

	app := Health{"catalogue", "OK", time.Now().String()}
	db := Health{"catalogue-db", dbstatus, time.Now().String()}
	health = append(health, app, db)
	return health
}

func cut(socks []Sock, pageNum, pageSize int) []Sock {
	if pageNum == 0 || pageSize == 0 {
		return []Sock{}
	}
	start := (pageNum * pageSize) - pageSize
	if start > len(socks) {
		return []Sock{}
	}
	end := (pageNum * pageSize)
	if end > len(socks) {
		end = len(socks)
	}
	return socks[start:end]
}
