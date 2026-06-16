FROM php:8.3-cli-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY public public
RUN mkdir -p /app/config /srv/app-artifacts/storage/logs \
  && ln -s /srv/app-artifacts/storage /app/storage \
  && printf 'FIXED\n' > /app/config/app.key \
  && touch /srv/app-artifacts/storage/logs/audit.log \
  && chown app:app /app/config/app.key \
  && chmod 0644 /app/config/app.key \
  && chown -R root:root /srv/app-artifacts \
  && chmod 0664 /srv/app-artifacts/storage/logs/audit.log \
  && chmod 0555 /srv/app-artifacts/storage/logs

USER app

CMD ["php", "-S", "0.0.0.0:8080", "-t", "public"]
