import os

from fastapi import FastAPI

app = FastAPI()


@app.get("/orders/{order_id}")
def get_order(order_id: str):
    if os.getenv("FAULT_MODE") == "runtime_error":
        raise RuntimeError("database unavailable")

    return {"status": "BROKEN", "order_id": order_id}
