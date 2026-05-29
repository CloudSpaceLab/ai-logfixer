import os

from django.http import HttpResponse


def orders(request, order_id):
    if os.environ.get("FAULT_MODE") == "runtime_error":
        raise RuntimeError("database unavailable")
    return HttpResponse("BROKEN")
