FROM composer:2 AS deps
WORKDIR /app
COPY composer.json composer.json
RUN composer install --no-interaction --prefer-dist --no-scripts || true

FROM php:8.3-cli
WORKDIR /app
COPY --from=deps /app /app
COPY . /app
ENV FAULT_MODE=runtime_error
EXPOSE 8080
CMD ["php", "-S", "0.0.0.0:8080", "-t", "public"]
