FROM golang:1.22-alpine AS base
WORKDIR /src
COPY go.mod go.sum ./
COPY remote-config-api ./remote-config-api
COPY remote-config-sdk ./remote-config-sdk
COPY remoteconfig ./remoteconfig
COPY examples/app ./examples/app
RUN go mod download

FROM base AS api-build
RUN CGO_ENABLED=0 go build -o /out/remote-config-api ./remote-config-api/cmd/api
RUN CGO_ENABLED=0 go build -o /out/remote-config-token ./remote-config-api/cmd/token

FROM alpine:3.20 AS api
RUN adduser -D appuser
USER appuser
COPY --from=api-build /out/remote-config-api /usr/local/bin/remote-config-api
COPY --from=api-build /out/remote-config-token /usr/local/bin/remote-config-token
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/remote-config-api"]

FROM base AS app-build
RUN CGO_ENABLED=0 go build -o /out/app ./examples/app/cmd/app

FROM alpine:3.20 AS app
RUN adduser -D appuser
USER appuser
COPY --from=app-build /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
