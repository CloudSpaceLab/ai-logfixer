from pathlib import Path

from flask import Flask, jsonify


app = Flask(__name__)


@app.get("/orders/readiness")
def readiness():
    try:
        Path("templates/readiness.txt").read_text()
        with Path("instance/app.sqlite").open("a") as handle:
            handle.write("readiness audit\n")
    except OSError as error:
        return jsonify({"detail": f"permission drift: {error}"}), 500
    return jsonify({"status": "FIXED", "lane": "permission-drift"})


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=8080)
