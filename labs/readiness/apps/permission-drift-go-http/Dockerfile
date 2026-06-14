FROM golang:1.23-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY main.go main.go
RUN go build -o /server main.go \
  && mkdir -p /app/data \
  && chown -R root:root /app/data \
  && chmod 0555 /app/data

USER app

CMD ["/server"]
