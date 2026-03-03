package catalogue

import (
	"context"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
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

func (mw loggingMiddleware) logWithTrace(ctx context.Context, keyvals ...interface{}) {
	args := append(TraceFieldsFromContext(ctx), keyvals...)
	_ = mw.logger.Log(args...)
}

func (mw loggingMiddleware) List(ctx context.Context, tags []string, order string, pageNum, pageSize int) (socks []Sock, err error) {
	defer func(begin time.Time) {
		mw.logWithTrace(ctx,
			"operation", "List",
			"tags", strings.Join(tags, ", "),
			"order", order,
			"page_num", pageNum,
			"page_size", pageSize,
			"result_count", len(socks),
			"latency_ms", time.Since(begin).Milliseconds(),
			"level", "info",
		)
	}(time.Now())
	return mw.next.List(ctx, tags, order, pageNum, pageSize)
}

func (mw loggingMiddleware) Count(ctx context.Context, tags []string) (n int, err error) {
	defer func(begin time.Time) {
		mw.logWithTrace(ctx,
			"operation", "Count",
			"tags", strings.Join(tags, ", "),
			"result_count", n,
			"latency_ms", time.Since(begin).Milliseconds(),
			"level", "info",
		)
	}(time.Now())
	return mw.next.Count(ctx, tags)
}

func (mw loggingMiddleware) Get(ctx context.Context, id string) (s Sock, err error) {
	defer func(begin time.Time) {
		mw.logWithTrace(ctx,
			"operation", "Get",
			"id", id,
			"sock_id", s.ID,
			"latency_ms", time.Since(begin).Milliseconds(),
			"level", "info",
		)
	}(time.Now())
	return mw.next.Get(ctx, id)
}

func (mw loggingMiddleware) Tags(ctx context.Context) (tags []string, err error) {
	defer func(begin time.Time) {
		mw.logWithTrace(ctx,
			"operation", "Tags",
			"result_count", len(tags),
			"latency_ms", time.Since(begin).Milliseconds(),
			"level", "info",
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
