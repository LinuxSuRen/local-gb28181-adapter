BINARY := bin/local-gb28181-adapter
WEB_DIR := web
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)

.PHONY: setup build build-web build-go run test test-go lint release snapshot up down logs ps clean version help

help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

version: ## 显示版本
	@echo $(VERSION)

setup: ## 安装前端依赖
	cd $(WEB_DIR) && npm install

build-web: ## 构建前端（产物 web/dist）
	cd $(WEB_DIR) && npm run build

build-go: ## 编译后端单二进制（内嵌前端）
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/local-gb28181-adapter

build: build-web build-go ## 构建前端 + 后端

run: build ## 本地运行（需本机 ffmpeg）
	$(BINARY)

test-go: ## Go 单元测试
	go test -race ./...

test-web: ## 前端构建校验
	cd $(WEB_DIR) && npm run build

test: test-go ## 全量测试
lint: ## go vet
	go vet ./...

release: ## 通过 goreleaser 发布（tag 触发，需 goreleaser 与 GITHUB_TOKEN）
	goreleaser release --clean

snapshot: ## 本地验证发布产物（不打 tag、不上传）
	goreleaser build --snapshot --clean

up: ## 一键启动（docker compose，host 网络，单容器）
	docker compose up -d --build

down: ## 停止并移除容器
	docker compose down

logs: ## 跟随查看容器日志
	docker compose logs -f

ps: ## 查看容器状态
	docker compose ps

clean: ## 清理构建产物
	rm -rf bin $(WEB_DIR)/dist
