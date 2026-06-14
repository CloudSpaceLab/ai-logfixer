FROM node:22-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY package.json package.json
RUN npm install --omit=dev
COPY src src
RUN mkdir -p /app/config /app/uploads \
  && printf '{"status":"FIXED"}\n' > /app/config/readiness.json \
  && touch /app/uploads/audit.log \
  && chown app:app /app/config/readiness.json \
  && chmod 0644 /app/config/readiness.json \
  && chown -R root:root /app/uploads \
  && chmod 0664 /app/uploads/audit.log \
  && chmod 0555 /app/uploads

USER app

CMD ["node", "src/server.js"]
