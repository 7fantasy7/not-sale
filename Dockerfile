FROM golang:1.24 AS builder

WORKDIR /src
COPY . .
ARG SERVICE_NAME=not-sale-back
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/service ./cmd/app

FROM gcr.io/distroless/static-debian12
WORKDIR /app
ARG SERVICE_NAME=not-sale-back
COPY --from=builder /out/service /app/service
EXPOSE 8080
USER nonroot:nonroot
CMD ["/app/service"]
