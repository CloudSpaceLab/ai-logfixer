FROM golang:1.23-alpine

WORKDIR /app
COPY main.go main.go
COPY . /app
RUN go build -o /server main.go && chmod +x /app/start.sh

CMD ["/app/start.sh"]
