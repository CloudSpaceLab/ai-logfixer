FROM python:3.12-slim
WORKDIR /app
COPY pyproject.toml pyproject.toml
RUN pip install --no-cache-dir fastapi uvicorn
COPY . /app
EXPOSE 8080
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8080"]
