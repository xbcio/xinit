# xinit 的构建入口：只做薄封装，不引入 Go 之外的工具。
#
# 常用：
#   make build   构建到 bin/xinit
#   make check   提交前跑这个（fmt + vet + test）

BIN ?= bin/xinit

.PHONY: build install test vet fmt check clean crosswin

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

# crosswin：Windows 交叉编译门禁（M11 §22.13）；无需 Windows 机器。
# 逐包 go test -c 覆盖全部测试文件的 Windows 编译面（含 txtar 宿主代码）。
crosswin:
	GOOS=windows GOARCH=amd64 go build ./...
	GOOS=windows GOARCH=arm64 go build ./...
	GOOS=windows go vet ./...
	@for pkg in $$(go list ./...); do \
		GOOS=windows go test -c -o /dev/null "$$pkg" || exit 1; \
	done
	@echo "crosswin: OK（14 包）"

clean:
	rm -rf bin/
