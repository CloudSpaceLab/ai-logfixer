FROM ruby:3.3-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY server.rb server.rb
RUN mkdir -p /app/storage \
  && chown -R root:root /app/storage \
  && chmod 0555 /app/storage

USER app

CMD ["ruby", "server.rb"]
