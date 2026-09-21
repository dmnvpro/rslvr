APP_NAME  = rslvr
BIN_DIR   = bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   = -s -w -X rslvr/internal/app.Version=$(VERSION)

all: build

build: ## build for the host platform into bin/rslvr
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(APP_NAME) ./cmd/rslvr

build-linux: ## CGO_ENABLED=0 linux/amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BIN_DIR)/$(APP_NAME)-linux-amd64 ./cmd/rslvr

build-win: ## CGO_ENABLED=0 windows/amd64
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BIN_DIR)/$(APP_NAME)-windows-amd64.exe ./cmd/rslvr

cross: build-linux build-win

run: build
	./$(BIN_DIR)/$(APP_NAME)

vet:
	go vet ./...

test:
	go test ./... -timeout 120s

race:
	go test -race ./... -timeout 240s

cover: ## coverage of the three gated packages (target >= 70%)
	go test ./internal/config/ ./internal/vision/ ./internal/capture/ \
		-coverprofile=cover.out -timeout 180s
	go tool cover -func=cover.out | tail -1

lint: ## install golangci-lint on first use
	@command -v golangci-lint >/dev/null 2>&1 || \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.62.2
	golangci-lint run

verify: vet test race ## everything CI needs

e2e-prepare: ## load the fake camera (needs sudo) and feed it a test pattern
	sudo modprobe v4l2loopback video_nr=0 card_label="rslvr test cam" exclusive_caps=1
	ffmpeg -hide_banner -loglevel error -f lavfi \
		-i testsrc=size=1280x720:rate=30 -f v4l2 /dev/video0 &
	sleep 1

e2e: build ## real capture against the loopback camera from e2e-prepare
	go test -tags e2e ./internal/e2e/ -v -timeout 180s

clean:
	rm -rf $(BIN_DIR) cover.out

.PHONY: all build build-linux build-win cross run vet test race cover lint verify e2e-prepare e2e clean
