FROM golang:1.22-alpine AS builder

WORKDIR /build

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -mod=mod -a -installsuffix cgo -o /app github.com/microservices-demo/catalogue/cmd/cataloguesvc

FROM alpine:3.19

ENV	SERVICE_USER=myuser \
	SERVICE_UID=10001 \
	SERVICE_GROUP=mygroup \
	SERVICE_GID=10001

RUN apk add --no-cache libcap

RUN	addgroup -g ${SERVICE_GID} ${SERVICE_GROUP} && \
	adduser -g "${SERVICE_NAME} user" -D -H -G ${SERVICE_GROUP} -s /sbin/nologin -u ${SERVICE_UID} ${SERVICE_USER}

WORKDIR /
COPY --from=0 /app /app
COPY images/ /images/

RUN	chmod +x /app && \
  setcap 'cap_net_bind_service=+ep' /app && \
	chown -R ${SERVICE_USER}:${SERVICE_GROUP} /app /images

# USER ${SERVICE_USER}
USER root

ARG BUILD_DATE
ARG BUILD_VERSION
ARG COMMIT

LABEL org.label-schema.vendor="Weaveworks" \
  org.label-schema.build-date="${BUILD_DATE}" \
  org.label-schema.version="${BUILD_VERSION}" \
  org.label-schema.name="Socks Shop: Catalogue" \
  org.label-schema.description="REST API for Catalogue service" \
  org.label-schema.url="https://github.com/microservices-demo/catalogue" \
  org.label-schema.vcs-url="github.com:microservices-demo/catalogue.git" \
  org.label-schema.vcs-ref="${COMMIT}" \
  org.label-schema.schema-version="1.0"

CMD ["/app", "-port=80"]
EXPOSE 80

#catalogue:inject-1-2-v2