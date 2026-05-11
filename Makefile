.PHONY: docker-up docker-down run-admin run-demo run-all test vet tidy swagger-install swagger-gen swagger-gen-full lint

docker-up:
	docker-compose up -d

docker-down:
	docker-compose down

run-all:
	docker-compose up -d redis
	go run ./cmd/admin-api

run-admin:
	go run ./cmd/admin-api

run-demo:
	cd demo && make run

swagger-install:
	go install github.com/swaggo/swag/cmd/swag@latest

swagger-gen:
	swag init -g cmd/admin-api/main.go -o docs --parseInternal

swagger-gen-full:
	swag init -g cmd/admin-api/main.go -o docs --parseInternal --parseDependency

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

lint:
	golangci-lint run ./...
