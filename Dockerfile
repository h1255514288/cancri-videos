FROM golang:1.26-alpine AS builder
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/control ./cmd/control && CGO_ENABLED=0 go build -o /out/node ./cmd/node && CGO_ENABLED=0 go build -o /out/bootstrap-admin ./cmd/bootstrap-admin

FROM alpine:3.22
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app && mkdir -p /var/video-distribution/storage && chown app:app /var/video-distribution/storage
COPY --from=builder /out/ /usr/local/bin/
USER app
EXPOSE 8080 8001
CMD ["control"]
