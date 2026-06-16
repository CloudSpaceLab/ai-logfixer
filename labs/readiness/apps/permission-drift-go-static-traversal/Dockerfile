FROM golang:1.23-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app
COPY main.go main.go
RUN go build -o /server main.go \
  && mkdir -p /app/public \
  && printf 'static readiness asset\n' > /app/public/status.txt \
  && chown -R root:root /app/public \
  && chmod 0644 /app/public/status.txt \
  && chmod 0000 /app/public

USER app
EXPOSE 8080
CMD ["/server"]
