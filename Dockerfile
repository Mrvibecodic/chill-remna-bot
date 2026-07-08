FROM golang:1.25.12-alpine AS build
ARG COMMIT=dev
ARG BUILD_DATE=
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" -o /out/bot ./cmd/bot

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata docker-cli docker-cli-compose
WORKDIR /app
COPY --from=build /out/bot /app/bot
VOLUME ["/data"]
ENV DATA_DIR=/data
ENTRYPOINT ["/app/bot"]
