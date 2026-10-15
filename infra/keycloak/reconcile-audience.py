#!/usr/bin/env python3
"""Idempotently add the API client and gateway audience mapper; preserve realm/users.
Requires KEYCLOAK_ADMIN_USERNAME/PASSWORD in the process environment.
"""
import json
import os
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request

BASE = "http://localhost:8180"


def request(path, method="GET", data=None, token=None, form=False):
    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if data is not None:
        headers["Content-Type"] = "application/x-www-form-urlencoded" if form else "application/json"
        data = (urllib.parse.urlencode(data) if form else json.dumps(data)).encode()
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=5) as response:
        body = response.read()
        return json.loads(body) if body else None


def main():
    token = request("/realms/master/protocol/openid-connect/token", "POST", {
        "grant_type": "password", "client_id": "admin-cli",
        "username": os.environ["KEYCLOAK_ADMIN_USERNAME"],
        "password": os.environ["KEYCLOAK_ADMIN_PASSWORD"],
    }, form=True)["access_token"]
    prefix = "/admin/realms/cartside/clients"
    template = json.loads(Path(__file__).with_name("cartside-realm.json").read_text())
    api = next(c for c in template["clients"] if c["clientId"] == "cartside-api")
    existing = request(prefix + "?clientId=cartside-api", token=token)
    if existing:
        request(prefix + "/" + existing[0]["id"], "PUT", api, token)
    else:
        request(prefix, "POST", api, token)
    gateway = request(prefix + "?clientId=cartside-apisix", token=token)
    if len(gateway) != 1:
        raise ValueError("gateway client missing")
    mapper = template["clients"][0]["protocolMappers"][0]
    path = prefix + "/" + gateway[0]["id"] + "/protocol-mappers/models"
    existing = [m for m in request(path, token=token) if m["name"] == mapper["name"]]
    if existing:
        mapper["id"] = existing[0]["id"]
        request(path + "/" + mapper["id"], "PUT", mapper, token)
    else:
        request(path, "POST", mapper, token)
    print("Cartside API audience reconciled; renew browser sessions to obtain new claims.")


if __name__ == "__main__":
    try:
        main()
    except (KeyError, ValueError, OSError, urllib.error.URLError):
        # Never print provider responses, credentials, or token-bearing requests.
        raise SystemExit("Audience reconciliation failed; check admin configuration and Keycloak availability.") from None
