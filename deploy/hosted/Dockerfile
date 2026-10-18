FROM node:22-alpine AS web
WORKDIR /source/console/web
COPY console/web/package*.json ./
RUN npm ci
COPY console/web/ ./
RUN npm run type-check && npm run build

FROM golang:1.25.3-alpine AS build
WORKDIR /source
COPY go.mod go.sum ./
RUN go mod download
COPY protocol/ ./protocol/
COPY runtime/ ./runtime/
COPY console/embed.go ./console/embed.go
COPY --from=web /source/console/dist/ ./console/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/wia-worker ./runtime/cmd/worker
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/wia-hosted ./runtime/cmd/hosted

FROM alpine:3.22 AS worker
RUN apk add --no-cache ca-certificates && mkdir /data && chown 65532:65532 /data
COPY --from=build /out/wia-worker /usr/local/bin/wia-worker
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/wia-worker"]

FROM docker:28-cli AS hosted
RUN apk add --no-cache ca-certificates
COPY --from=build /out/wia-hosted /usr/local/bin/wia-hosted
ENTRYPOINT ["/usr/local/bin/wia-hosted"]
CMD ["-config", "/etc/wia/hosted.json"]
