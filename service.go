package catalogue

import (
	"errors"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/jmoiron/sqlx"
	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"golang.org/x/net/context"
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

func NewCatalogueService(db *sqlx.DB, logger log.Logger) Service {
	return &catalogueService{db: db, logger: logger}
}

type catalogueService struct {
	db     *sqlx.DB
	logger log.Logger
}

func startDBSpan(ctx context.Context, operation, statement string) opentracing.Span {
	span := opentracing.SpanFromContext(ctx)
	if span == nil {
		span = opentracing.StartSpan(operation)
	} else {
		span = opentracing.StartSpan(operation, opentracing.ChildOf(span.Context()))
	}
	ext.SpanKindRPCClient.Set(span)
	span.SetTag("db.type", "mysql")
	if statement != "" {
		span.SetTag("db.statement", statement)
	}
	return span
}

func (s *catalogueService) List(ctx context.Context, tags []string, order string, pageNum, pageSize int) ([]Sock, error) {
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

	span := startDBSpan(ctx, "mysql SELECT catalogue list", query)
	defer span.Finish()

	err := s.db.Select(&socks, query, args...)
	if err != nil {
		ext.Error.Set(span, true)
		span.LogKV("event", "error", "message", err.Error())
		s.logger.Log("database error", err)
		return []Sock{}, ErrDBConnection
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

	span := startDBSpan(ctx, "mysql SELECT catalogue count", query)
	defer span.Finish()

	stmt, err := s.db.Prepare(query)
	if err != nil {
		ext.Error.Set(span, true)
		span.LogKV("event", "error", "message", err.Error())
		s.logger.Log("database error", err)
		return 0, ErrDBConnection
	}
	defer stmt.Close()

	var count int
	if err := stmt.QueryRow(args...).Scan(&count); err != nil {
		ext.Error.Set(span, true)
		span.LogKV("event", "error", "message", err.Error())
		s.logger.Log("database error", err)
		return 0, ErrDBConnection
	}

	return count, nil
}

func (s *catalogueService) Get(ctx context.Context, id string) (Sock, error) {
	query := baseQuery + " WHERE sock.sock_id =? GROUP BY sock.sock_id;"

	span := startDBSpan(ctx, "mysql SELECT catalogue get", query)
	span.SetTag("db.id", id)
	defer span.Finish()

	var sock Sock
	err := s.db.Get(&sock, query, id)
	if err != nil {
		ext.Error.Set(span, true)
		span.LogKV("event", "error", "message", err.Error())
		s.logger.Log("database error", err)
		return Sock{}, ErrNotFound
	}

	sock.ImageURL = []string{sock.ImageURL_1, sock.ImageURL_2}
	sock.Tags = strings.Split(sock.TagString, ",")

	return sock, nil
}

func (s *catalogueService) Tags(ctx context.Context) ([]string, error) {
	query := "SELECT name FROM tag;"

	span := startDBSpan(ctx, "mysql SELECT catalogue tags", query)
	defer span.Finish()

	rows, err := s.db.Query(query)
	if err != nil {
		ext.Error.Set(span, true)
		span.LogKV("event", "error", "message", err.Error())
		s.logger.Log("database error", err)
		return []string{}, ErrDBConnection
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			ext.Error.Set(span, true)
			span.LogKV("event", "row_scan_error", "message", err.Error())
			s.logger.Log("database error", err)
			continue
		}
		tags = append(tags, tag)
	}

	return tags, nil
}

func (s *catalogueService) Health(ctx context.Context) []Health {
	var health []Health
	dbstatus := "OK"

	span := startDBSpan(ctx, "mysql PING catalogue-db", "PING")
	defer span.Finish()

	if err := s.db.Ping(); err != nil {
		dbstatus = "err"
		ext.Error.Set(span, true)
		span.LogKV("event", "error", "message", err.Error())
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
