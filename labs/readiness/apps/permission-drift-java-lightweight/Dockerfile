FROM eclipse-temurin:21-jdk-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY App.java App.java
RUN javac App.java \
  && mkdir -p /app/config /app/logs \
  && printf 'FIXED\n' > /app/config/cert.pem \
  && touch /app/logs/audit.log \
  && chown app:app /app/config/cert.pem \
  && chmod 0644 /app/config/cert.pem \
  && chown -R root:root /app/logs \
  && chmod 0664 /app/logs/audit.log \
  && chmod 0555 /app/logs

USER app

CMD ["java", "App"]
