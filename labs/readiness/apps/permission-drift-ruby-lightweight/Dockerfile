FROM ruby:3.3-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY server.rb server.rb
RUN mkdir -p /app/public /app/storage \
  && printf 'FIXED\n' > /app/public/status.txt \
  && touch /app/storage/audit.log \
  && chown app:app /app/public/status.txt \
  && chmod 0644 /app/public/status.txt \
  && chown -R root:root /app/storage \
  && chmod 0664 /app/storage/audit.log \
  && chmod 0555 /app/storage

USER app

CMD ["ruby", "server.rb"]
