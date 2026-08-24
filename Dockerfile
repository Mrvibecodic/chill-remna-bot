# Сборка идёт на архитектуре раннера, бинарник кросс-компилируется под целевую.
FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine AS build
ARG COMMIT=dev
ARG BUILD_DATE=
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" -o /out/bot ./cmd/bot

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata docker-cli docker-cli-compose
WORKDIR /app
COPY --from=build /out/bot /app/bot
# Уведомления о лицензиях едут вместе с бинарником (требование MIT/BSD).
COPY LICENSE THIRD-PARTY-NOTICES.md /app/
VOLUME ["/data"]
ENV DATA_DIR=/data
ENTRYPOINT ["/app/bot"]
