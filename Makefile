.PHONY: build run test test-cover lint fmt vet tidy generate migrate-diff migrate-apply clean docker-build fly-deploy deploy-fleet deploy-fleet-check

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

# Deploy the whole ingest fleet (publisher + all region ingesters) so they stay
# on the same image. CI only deploys seestorm-ingest; run this after a merge to
# ship the regionals too. Role/region come from each app's durable Fly secrets.
deploy-fleet:
	./scripts/deploy-fleet.sh

deploy-fleet-check:
	./scripts/deploy-fleet.sh --check
