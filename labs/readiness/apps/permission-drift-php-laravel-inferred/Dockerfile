FROM php:8.3-cli-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY public public
RUN mkdir -p /app/config /app/storage/logs \
  && printf 'FIXED\n' > /app/config/app.key \
  && chown app:app /app/config/app.key \
  && chmod 0644 /app/config/app.key \
  && chown -R root:root /app/storage \
  && chmod 0555 /app/storage/logs

USER app

CMD ["php", "-S", "0.0.0.0:8080", "-t", "public"]
