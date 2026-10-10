# xinit 的构建入口：只做薄封装，不引入 Go 之外的工具。
#
# 常用：
#   make build   构建到 bin/xinit
#   make check   提交前跑这个（fmt + vet + test）

BIN ?= bin/xinit

.PHONY: build install test vet fmt check clean

build:
	go build -o $(BIN) .

install:
	go install .

test:
	go test ./... -count=1

vet:
	go vet ./...

# gofmt -l 有输出即失败
fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt 报告以下文件未格式化："; \
		echo "$$unformatted"; \
		exit 1; \
	fi

check: fmt vet test

clean:
	rm -rf bin/
