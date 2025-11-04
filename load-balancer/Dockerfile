    # Stage 1: Build
    FROM golang:latest AS builder
    WORKDIR /app
    COPY go.mod ./
    RUN go mod download
    COPY . .
    RUN CGO_ENABLED=0 GOOS=linux go build -o /loadbalancer ./main.go
    
    # Stage 2: Final Image
    FROM alpine:latest
    WORKDIR /
    COPY --from=builder /loadbalancer /loadbalancer
    EXPOSE 8080
    CMD ["/loadbalancer"]
