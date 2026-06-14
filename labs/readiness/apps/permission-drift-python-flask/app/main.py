from pathlib import Path

from flask import Flask, jsonify


app = Flask(__name__)


@app.get("/orders/readiness")
def readiness():
    try:
        Path("instance/audit.log").write_text("readiness audit\n")
    except OSError as error:
        return jsonify({"detail": f"permission drift: {error}"}), 500
    return jsonify({"status": "FIXED", "lane": "permission-drift"})


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=8080)
