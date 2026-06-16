FROM golang:1.23-alpine

RUN addgroup -g 10001 app \
  && addgroup -g 20001 fsgroup \
  && adduser -D -u 10001 -G app app \
  && addgroup app fsgroup
WORKDIR /app

COPY main.go main.go
RUN go build -o /server main.go \
  && mkdir -p /app/volume \
  && printf '{"status":"FIXED"}\n' > /app/volume/status.json \
  && touch /app/volume/events.log \
  && chown -R root:root /app/volume \
  && chmod 0750 /app/volume \
  && chmod 0640 /app/volume/status.json \
  && chmod 0660 /app/volume/events.log

USER app
EXPOSE 8080
CMD ["/server"]
