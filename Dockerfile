FROM golang:1.25.0-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY gen ./gen
COPY internal ./internal
COPY cmd ./cmd
ARG SERVICE
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /service ./cmd/${SERVICE}

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app
USER app
COPY --from=build /service /service
ENTRYPOINT ["/service"]
