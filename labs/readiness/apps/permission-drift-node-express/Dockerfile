FROM node:22-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY package.json package.json
RUN npm install --omit=dev
COPY src src
RUN mkdir -p /app/uploads \
  && chown -R root:root /app/uploads \
  && chmod 0555 /app/uploads

USER app

CMD ["node", "src/server.js"]
