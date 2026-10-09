.PHONY: help build test vet python-test integration compose-smoke clean run-control run-node docker-up docker-down

help:
	@echo "可用命令："
	@echo "  make build        - 编译所有服务"
	@echo "  make test         - 运行测试"
	@echo "  make clean        - 清理编译产物"
	@echo "  make run-control  - 运行 Control 服务"
	@echo "  make run-node     - 运行 Node 服务"
	@echo "  make vet/python-test/integration/compose-smoke - 分层验证"
	@echo "  make docker-up    - 构建并等待全部服务启动"
	@echo "  make docker-down  - 停止服务，保留数据卷"

build:
	go build -o bin/control ./cmd/control
	go build -o bin/node ./cmd/node
	go build -o bin/bootstrap-admin ./cmd/bootstrap-admin

test:
	go test ./... -count=1 -timeout=2m

vet:
	go vet ./...

python-test:
	python -m unittest discover -s scripts -p '*_test.py' -v

integration:
	go test -race -tags integration ./tests/integration -count=1 -v -timeout=3m

compose-smoke:
	python scripts/compose_smoke.py --fault-tests

clean:
	rm -rf bin/

run-control:
	./bin/control

run-node:
	./bin/node

docker-up:
	docker compose up -d --build --wait

docker-down:
	docker compose down
