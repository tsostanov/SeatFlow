.PHONY: generate test integration run

generate:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.9
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
	PATH="$(shell go env GOPATH)/bin:$$PATH" protoc --go_out=. --go_opt=module=github.com/tsostanov/SeatFlow --go-grpc_out=. --go-grpc_opt=module=github.com/tsostanov/SeatFlow api/booking/v1/platform.proto

test:
	go test ./...
	go vet ./...
	node --test tests/booking-session.test.mjs

integration:
	go test -race -tags=integration ./tests/... -count=1

run:
	docker compose up --build -d --wait
