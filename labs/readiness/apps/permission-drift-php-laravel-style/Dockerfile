FROM php:8.3-cli-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY public public
RUN mkdir -p /app/storage/logs \
  && chown -R root:root /app/storage \
  && chmod 0555 /app/storage/logs

USER app

CMD ["php", "-S", "0.0.0.0:8080", "-t", "public"]
