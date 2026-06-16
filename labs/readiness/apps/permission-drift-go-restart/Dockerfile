FROM golang:1.23-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY main.go main.go
RUN go build -o /server main.go \
  && mkdir -p /app/data \
  && printf '{"status":"FIXED"}\n' > /app/data/readiness.json \
  && touch /app/data/startup.lock \
  && chown -R root:root /app/data \
  && chmod 0644 /app/data/readiness.json \
  && chmod 0664 /app/data/startup.lock \
  && chmod 0555 /app/data

USER app

CMD ["/server"]
