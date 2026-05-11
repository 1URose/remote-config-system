FROM golang:1.22-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY docs ./docs
COPY internal ./internal
COPY pkg ./pkg

RUN CGO_ENABLED=0 go build -o /out/remote-config-admin-api ./cmd/admin-api
RUN CGO_ENABLED=0 go build -o /out/remote-config-token ./cmd/token

FROM alpine:3.20 AS api
RUN adduser -D appuser
USER appuser

COPY --from=build /out/remote-config-admin-api /usr/local/bin/remote-config-admin-api
COPY --from=build /out/remote-config-token /usr/local/bin/remote-config-token

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/remote-config-admin-api"]
