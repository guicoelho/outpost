FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /outbound-proxy .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /outbound-proxy /usr/local/bin/outbound-proxy
ENTRYPOINT ["outbound-proxy"]
