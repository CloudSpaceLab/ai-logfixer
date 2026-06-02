FROM python:3.12-slim

RUN useradd -u 10001 -m app
WORKDIR /app

COPY . /app
RUN chown -R app:app /app/app \
  && mkdir -p /app/storage/logs \
  && chown -R root:root /app/storage \
  && chmod 0555 /app/storage/logs

USER app

CMD ["python", "app/main.py"]
