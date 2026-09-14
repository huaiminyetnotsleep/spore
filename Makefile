.PHONY: dev build vet test tidy linux build-linux frontend-install frontend-build frontend-lint frontend-typecheck frontend-test docs-install docs-dev docs-build docs-preview

FRONTEND_DIR := frontend
FRONTEND_DIST := internal/web/frontend-dist
FRONTEND_INSTALL_STAMP := $(FRONTEND_DIR)/node_modules/.spore-install.stamp
DOCS_DIR := docs

# 本地开发运行
dev:
	go run ./cmd/bot

# 前端依赖安装：仅在依赖文件变更或 node_modules 缺失时执行
frontend-install: $(FRONTEND_INSTALL_STAMP)

$(FRONTEND_INSTALL_STAMP): $(FRONTEND_DIR)/package.json $(FRONTEND_DIR)/package-lock.json
	cd $(FRONTEND_DIR) && npm ci
	touch $@

frontend-build: frontend-install
	cd $(FRONTEND_DIR) && npm run build
	find $(FRONTEND_DIST) -mindepth 1 ! -name .gitkeep ! -name placeholder.txt -exec rm -rf {} +
	cp -R $(FRONTEND_DIR)/dist/. $(FRONTEND_DIST)/

frontend-lint: frontend-install
	cd $(FRONTEND_DIR) && npm run lint

frontend-typecheck: frontend-install
	cd $(FRONTEND_DIR) && npm run typecheck

frontend-test: frontend-install
	cd $(FRONTEND_DIR) && npm run test

# 文档站点（VitePress，依赖与内容都在 docs/ 内）
docs-install:
	cd $(DOCS_DIR) && npm install

docs-dev: docs-install
	cd $(DOCS_DIR) && npm run dev

docs-build: docs-install
	cd $(DOCS_DIR) && npm run build

docs-preview: docs-install
	cd $(DOCS_DIR) && npm run preview

build: frontend-build
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

tidy:
	go mod tidy

# Linux 二进制构建参数的唯一来源（Dockerfile 复用同一目标，避免参数漂移）
# 默认 amd64；Docker buildx 通过 GOARCH=arm64 覆盖。
GOARCH ?= amd64
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -ldflags="-s -w" -o spore-linux ./cmd/bot

# VPS 部署交叉编译；ARM 服务器改 GOARCH=arm64
linux: build-linux
