"""Exercise a disposable demo stack through its public HTTP endpoint."""

import json
import os
import time
import urllib.error
import urllib.request
import uuid

BASE = os.environ.get("SMOKE_BASE_URL", "http://localhost:8080")


def request(method, path, body=None, key=None, expected=200):
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request(
        BASE + path, method=method, headers=headers,
        data=json.dumps(body).encode() if body is not None else None,
    )
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read()
        assert response.status == expected, (method, path, response.status, raw)
        return json.loads(raw) if "application/json" in response.headers.get("Content-Type", "") else raw


request("GET", "/readyz")
assert b"SeatFlow" in request("GET", "/")
assert b"BookingSession" in request("GET", "/app.js")
assert b"BEGIN:VCALENDAR" in request("GET", "/ticket-calendar.mjs")
assert b"createTicketShare" in request("GET", "/ticket-share.mjs")
events = request("GET", "/api/events")["events"]
event_id = int(events[0]["id"])
available = request("GET", f"/api/events/{event_id}/seats")
seat_id = int(available["available_seat_ids"][0])
body = {"event_id": event_id, "seat_id": seat_id}
key = str(uuid.uuid4())
booking = request("POST", "/api/bookings", body, key)
assert request("POST", "/api/bookings", body, key)["id"] == booking["id"]
request("POST", "/api/bookings", body, str(uuid.uuid4()), expected=409)
path = f"/api/bookings/{booking['id']}"
request("POST", path + "/checkout", {"payment_result": "fail"}, expected=409)
assert request("GET", path)["status"] == "RESERVED"
assert request("DELETE", path)["status"] == "CANCELLED"
booking = request("POST", "/api/bookings", body, str(uuid.uuid4()))
path = f"/api/bookings/{booking['id']}"
assert request("POST", path + "/checkout", {"payment_result": "success"})["status"] == "SOLD"
assert request("POST", path + "/checkout", {"payment_result": "success"})["status"] == "SOLD"
request("DELETE", path, expected=409)

# CI sets BOOKING_TTL=5s. Verify the worker and the configured TTL through TCP.
seat_id = int(request("GET", f"/api/events/{event_id}/seats")["available_seat_ids"][0])
booking = request("POST", "/api/bookings", {"event_id": event_id, "seat_id": seat_id}, str(uuid.uuid4()))
deadline = time.monotonic() + 20
while request("GET", f"/api/bookings/{booking['id']}")["status"] == "RESERVED":
    assert time.monotonic() < deadline, "Expected a short TTL; run the disposable stack with BOOKING_TTL=5s"
    time.sleep(0.25)
assert str(seat_id) in request("GET", f"/api/events/{event_id}/seats")["available_seat_ids"]
print("PASS: Compose readiness, assets, idempotency, conflict, decline, cancel, checkout, expiry")
