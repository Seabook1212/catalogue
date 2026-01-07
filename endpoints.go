package catalogue

import (
	"github.com/go-kit/kit/endpoint"
	"github.com/go-kit/kit/tracing/opentracing"
	stdopentracing "github.com/opentracing/opentracing-go"
	"golang.org/x/net/context"
)

type Endpoints struct {
	ListEndpoint   endpoint.Endpoint
	CountEndpoint  endpoint.Endpoint
	GetEndpoint    endpoint.Endpoint
	TagsEndpoint   endpoint.Endpoint
	HealthEndpoint endpoint.Endpoint
}

func MakeEndpoints(s Service, tracer stdopentracing.Tracer) Endpoints {
	return Endpoints{
		ListEndpoint:   opentracing.TraceServer(tracer, "GET /catalogue")(MakeListEndpoint(s)),
		CountEndpoint:  opentracing.TraceServer(tracer, "GET /catalogue/size")(MakeCountEndpoint(s)),
		GetEndpoint:    opentracing.TraceServer(tracer, "GET /catalogue/{id}")(MakeGetEndpoint(s)),
		TagsEndpoint:   opentracing.TraceServer(tracer, "GET /tags")(MakeTagsEndpoint(s)),
		HealthEndpoint: opentracing.TraceServer(tracer, "GET /health")(MakeHealthEndpoint(s)),
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
