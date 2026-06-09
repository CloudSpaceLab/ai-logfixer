FROM node:22-alpine

WORKDIR /app
COPY package.json package.json
COPY packages packages
RUN npm install --omit=dev

COPY . /app

CMD ["node", "src/server.js"]
