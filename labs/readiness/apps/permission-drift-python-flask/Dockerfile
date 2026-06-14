FROM python:3.12-slim

RUN useradd -u 10001 -m app
WORKDIR /app

COPY requirements.txt requirements.txt
RUN pip install --no-cache-dir -r requirements.txt
COPY app app
RUN mkdir -p /app/instance /app/templates \
  && printf 'FIXED\n' > /app/templates/readiness.txt \
  && touch /app/instance/app.sqlite \
  && chown app:app /app/templates/readiness.txt \
  && chmod 0644 /app/templates/readiness.txt \
  && chown -R root:root /app/instance \
  && chmod 0664 /app/instance/app.sqlite \
  && chmod 0555 /app/instance

USER app

CMD ["python", "app/main.py"]
