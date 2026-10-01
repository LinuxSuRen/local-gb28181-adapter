# ---- 前端构建 ----
FROM node:20-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- 后端构建 ----
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/embed.go
COPY --from=web /src/web/dist web/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/local-gb28181-adapter ./cmd/local-gb28181-adapter

# ---- 运行镜像 ----
FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tzdata
COPY --from=build /out/local-gb28181-adapter /usr/local/bin/local-gb28181-adapter
ENV DATA_DIR=/data
VOLUME /data
EXPOSE 8080 8554 5060/udp
ENTRYPOINT ["local-gb28181-adapter"]
