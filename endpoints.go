package catalogue

import (
	"context"

	"github.com/go-kit/kit/endpoint"
	stdopentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
)

type Endpoints struct {
	ListEndpoint   endpoint.Endpoint
	CountEndpoint  endpoint.Endpoint
	GetEndpoint    endpoint.Endpoint
	TagsEndpoint   endpoint.Endpoint
	HealthEndpoint endpoint.Endpoint
}

func MakeEndpoints(s Service, tracer stdopentracing.Tracer, traceTags TraceTags) Endpoints {
	return Endpoints{
		ListEndpoint:   traceServerEndpoint(tracer, traceTags, "GET /catalogue")(MakeListEndpoint(s)),
		CountEndpoint:  traceServerEndpoint(tracer, traceTags, "GET /catalogue/size")(MakeCountEndpoint(s)),
		GetEndpoint:    traceServerEndpoint(tracer, traceTags, "GET /catalogue/{id}")(MakeGetEndpoint(s)),
		TagsEndpoint:   traceServerEndpoint(tracer, traceTags, "GET /tags")(MakeTagsEndpoint(s)),
		HealthEndpoint: MakeHealthEndpoint(s), // No tracing for health endpoint
	}
}

func traceServerEndpoint(tracer stdopentracing.Tracer, traceTags TraceTags, operationName string) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (response interface{}, err error) {
			if serverSpan := stdopentracing.SpanFromContext(ctx); serverSpan != nil {
				traceTags.apply(serverSpan)
				// HTTP server spans created by transport are finished by transport finalizers.
				return next(ctx, request)
			}

			span := tracer.StartSpan(operationName, ext.SpanKindRPCServer)
			traceTags.apply(span)
			defer span.Finish()

			ctx = stdopentracing.ContextWithSpan(ctx, span)
			return next(ctx, request)
		}
	}
}

func MakeListEndpoint(s Service) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(listRequest)
		socks, err := s.List(ctx, req.Tags, req.Order, req.PageNum, req.PageSize)
		return listResponse{Socks: socks, Err: err}, err
	}
}

func MakeCountEndpoint(s Service) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(countRequest)
		n, err := s.Count(ctx, req.Tags)
		return countResponse{N: n, Err: err}, err
	}
}

func MakeGetEndpoint(s Service) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		req := request.(getRequest)
		sock, err := s.Get(ctx, req.ID)
		return getResponse{Sock: sock, Err: err}, err
	}
}

func MakeTagsEndpoint(s Service) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		tags, err := s.Tags(ctx)
		return tagsResponse{Tags: tags, Err: err}, err
	}
}

func MakeHealthEndpoint(s Service) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		health := s.Health(ctx)
		return healthResponse{Health: health}, nil
	}
}

type listRequest struct {
	Tags     []string `json:"tags"`
	Order    string   `json:"order"`
	PageNum  int      `json:"pageNum"`
	PageSize int      `json:"pageSize"`
}

type listResponse struct {
	Socks []Sock `json:"sock"`
	Err   error  `json:"err"`
}

type countRequest struct {
	Tags []string `json:"tags"`
}

type countResponse struct {
	N   int   `json:"size"`
	Err error `json:"err"`
}

type getRequest struct {
	ID string `json:"id"`
}

type getResponse struct {
	Sock Sock  `json:"sock"`
	Err  error `json:"err"`
}

type tagsResponse struct {
	Tags []string `json:"tags"`
	Err  error    `json:"err"`
}

type healthResponse struct {
	Health []Health `json:"health"`
}
