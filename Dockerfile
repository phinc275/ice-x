FROM golang:1.19-alpine as builder

WORKDIR /app
COPY ../go.mod go.sum ./
RUN go mod download
COPY .. ./
RUN go build -o migrate cmd/migrate/*.go
RUN go build -o ice-x cmd/ice-x/*.go

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/. ./
CMD /app/migrate auto && /app/ice-x serve