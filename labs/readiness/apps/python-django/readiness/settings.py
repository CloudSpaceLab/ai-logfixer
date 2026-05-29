import os


SECRET_KEY = "readiness-lab-only"
DEBUG = False
ROOT_URLCONF = "readiness.urls"
ALLOWED_HOSTS = ["*"]
DEFAULT_AUTO_FIELD = "django.db.models.BigAutoField"

INSTALLED_APPS = [
    "orders",
]

MIDDLEWARE = []

DATABASES = {
    "default": {
        "ENGINE": "django.db.backends.sqlite3",
        "NAME": os.environ.get("SQLITE_PATH", ":memory:"),
    }
}
