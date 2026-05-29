FROM python:3.12-slim

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY . .

ENV FAULT_MODE=runtime_error
EXPOSE 8080
CMD ["python", "manage.py", "runserver", "0.0.0.0:8080"]
