FROM golang:1.24.8-alpine AS build

ARG MINIO_VERSION=RELEASE.2025-10-15T17-29-55Z

ENV CGO_ENABLED=0

RUN go install github.com/minio/minio@${MINIO_VERSION}

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

COPY --from=build /go/bin/minio /usr/local/bin/minio

ENTRYPOINT ["/usr/local/bin/minio"]
