FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -o rss-griddle .

FROM alpine:3.21
RUN apk add --no-cache wget ca-certificates
COPY --from=build /app/rss-griddle /rss-griddle
EXPOSE 4080
CMD ["/rss-griddle"]
