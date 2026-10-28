FROM golang:1.25.12-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/triple-s . && \
    CGO_ENABLED=0 go build -trimpath -o /out/triple-s-cluster ./cmd/triple-s-cluster && \
    CGO_ENABLED=0 go build -trimpath -o /out/triple-s-backup ./cmd/triple-s-backup
FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 triples && adduser -D -u 10001 -G triples triples
COPY --from=build /out/ /usr/local/bin/
USER 10001:10001
WORKDIR /tmp
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/triple-s"]
