.PHONY: build run test test-cover lint fmt vet tidy generate migrate-diff migrate-apply clean docker-build fly-deploy

build:
	go build -o bin/ingest ./cmd/ingest

run:
	go run ./cmd/ingest

test:
	go test -race ./...

test-cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

lint:
	golangci-lint run

fmt:
	gofmt -w .
	goimports -w .

vet:
	go vet ./...

tidy:
	go mod tidy

generate:
	go generate ./ent/...

migrate-diff:
	atlas migrate diff --env local

migrate-apply:
	atlas migrate apply --env prod

clean:
	rm -rf bin/ coverage.out ingest.exe

docker-build:
	docker build -t seestorm-ingest .

fly-deploy:
	flyctl deploy --remote-only
