FROM golang:1.23-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY main.go main.go
RUN go build -o /server main.go \
  && mkdir -p /app/config /app/data \
  && printf '{"status":"FIXED"}\n' > /app/config/readiness.json \
  && touch /app/data/app.sqlite \
  && chown app:app /app/config/readiness.json \
  && chmod 0644 /app/config/readiness.json \
  && chown -R root:root /app/data \
  && chmod 0664 /app/data/app.sqlite \
  && chmod 0555 /app/data

USER app

CMD ["/server"]
