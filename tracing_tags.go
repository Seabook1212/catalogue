package catalogue

import (
	"os"

	stdopentracing "github.com/opentracing/opentracing-go"
)

// TraceTags are static span tags loaded once from pod environment variables.
type TraceTags struct {
	container string
	pod       string
	namespace string
	node      string
}

// NewTraceTagsFromEnv loads Kubernetes metadata tags from standard env vars.
func NewTraceTagsFromEnv() TraceTags {
	return TraceTags{
		container: os.Getenv("CONTAINER_NAME"),
		pod:       os.Getenv("POD_NAME"),
		namespace: os.Getenv("POD_NAMESPACE"),
		node:      os.Getenv("NODE_NAME"),
	}
}

func (t TraceTags) apply(span stdopentracing.Span) {
	if span == nil {
		return
	}
	if t.container != "" {
		span.SetTag("container", t.container)
	}
	if t.pod != "" {
		span.SetTag("pod", t.pod)
	}
	if t.namespace != "" {
		span.SetTag("namespace", t.namespace)
	}
	if t.node != "" {
		span.SetTag("node", t.node)
	}
}
