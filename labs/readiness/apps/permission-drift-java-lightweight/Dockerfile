FROM eclipse-temurin:21-jdk-alpine

RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app

COPY App.java App.java
RUN javac App.java \
  && mkdir -p /app/logs \
  && chown -R root:root /app/logs \
  && chmod 0555 /app/logs

USER app

CMD ["java", "App"]
