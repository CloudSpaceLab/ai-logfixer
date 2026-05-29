FROM node:22-alpine
WORKDIR /app
COPY package.json package.json
RUN npm install --omit=dev
COPY . /app
EXPOSE 8080
CMD ["npm", "start"]
