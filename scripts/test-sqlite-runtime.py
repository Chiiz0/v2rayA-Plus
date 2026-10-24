#!/usr/bin/env python3
"""Exercise the actual service API, restart, backup and rollback in isolation.

Requires Docker and two static Linux binaries built from service/:
  CGO_ENABLED=0 go build -o /tmp/v2raya-runtime .
  CGO_ENABLED=0 go build -o /tmp/v2raya-runtime-db ./cmd/v2raya-db
  python3 scripts/test-sqlite-runtime.py --service /tmp/v2raya-runtime \
      --db /tmp/v2raya-runtime-db

Only uniquely named test containers and temporary directories are used. No host
ports, host networking, Docker socket, host DNS or existing volumes are mounted.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid


def command(args, *, required=True):
    result = subprocess.run(args, capture_output=True, text=True, timeout=40)
    if required and result.returncode:
        # HTTP request arguments can contain an ephemeral password/token.
        raise RuntimeError("isolated runtime test command failed: " + args[0])
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--service", type=Path, required=True)
    parser.add_argument("--db", type=Path, required=True)
    parser.add_argument("--image", default="alpine:3.23")
    args = parser.parse_args()
    args.service, args.db = args.service.resolve(strict=True), args.db.resolve(strict=True)
    command(["docker", "image", "inspect", args.image])
    username, password = "sqlite-runtime-test", secrets.token_hex(12)
    containers = []

    def cli(operation, directory, *extra, required=True):
        result = command([str(args.db), operation, "--config-dir", str(directory), *extra], required=required)
        return json.loads(result.stdout) if result.returncode == 0 else None

    def request(name, path, token=None, data=None, required=True):
        wget = ["docker", "exec", name, "wget", "-q", "-T", "3", "-O", "-"]
        if token:
            wget += ["--header", "Authorization: Bearer " + token]
        if data is not None:
            wget += ["--header", "Content-Type: application/json", "--post-data", json.dumps(data)]
        result = command(wget + ["http://127.0.0.1:2017" + path], required=required)
        if not required:
            return result.returncode == 0
        response = json.loads(result.stdout)
        if response.get("code") != "SUCCESS":
            raise RuntimeError("isolated service rejected API request: " + path)
        return response["data"]

    def start(directory):
        name = "v2raya-sqlite-test-" + uuid.uuid4().hex[:12]
        containers.append(name)
        command(["docker", "run", "-d", "--name", name, "--network", "none", "--read-only",
                 "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                 "--user", f"{os.getuid()}:{os.getgid()}", "--tmpfs", "/tmp:rw,mode=1777",
                 "--mount", f"type=bind,src={directory},dst=/config",
                 "--mount", f"type=bind,src={assets},dst=/assets,readonly",
                 "--mount", f"type=bind,src={args.service},dst=/app,readonly",
                 "--entrypoint", "/app", args.image, "--lite", "--config", "/config",
                 "--address", "127.0.0.1:2017", "--v2ray-bin", "/missing-test-core",
                 "--v2ray-assetsdir", "/assets"])
        for _ in range(50):
            if request(name, "/", required=False):
                return name
            if command(["docker", "inspect", "-f", "{{.State.Running}}", name]).stdout.strip() != "true":
                logs = command(["docker", "logs", "--tail", "12", name])
                raise RuntimeError("isolated service exited before becoming ready: " + logs.stdout + logs.stderr)
            time.sleep(0.1)
        raise RuntimeError("isolated service did not become ready")

    def stop(name):
        command(["docker", "stop", "-t", "10", name])
        command(["docker", "rm", name])
        containers.remove(name)

    def login(name):
        return request(name, "/api/login", data={"username": username, "password": password})["token"]

    with tempfile.TemporaryDirectory(prefix="v2raya-sqlite-http-") as temp:
        directory, restored = Path(temp) / "config", Path(temp) / "restored"
        assets = Path(temp) / "assets"
        assets.mkdir()
        # HTTP persistence tests never start a proxy core or parse geo data.
        # Empty fixture assets prevent startup from attempting a download.
        for filename in ("geoip.dat", "geosite.dat"):
            (assets / filename).touch()
        directory.mkdir(mode=0o700)
        restored.mkdir(mode=0o700)
        try:
            cli("runtime-init", directory)
            name = start(directory)
            token = request(name, "/api/account", data={"username": username, "password": password})["token"]
            request(name, "/api/import", token, {"url": "vless://11111111-1111-4111-8111-111111111111@127.0.0.1:443?security=none&type=ws&path=%2Ftest#SQLite-test"})
            request(name, "/api/outbound", token, {"outbound": "sql-测试"})
            touch = request(name, "/api/touch", token)
            outbounds = request(name, "/api/outbounds", token)
            ports = request(name, "/api/ports", token)
            if ports["socks5"] != 20170 or ports["http"] != 20171:
                raise RuntimeError("fresh SQLite application defaults were not initialized")
            if cli("runtime-verify", directory, required=False) is not None:
                raise RuntimeError("maintenance accepted a running service")
            stop(name)
            name = start(directory)
            token = login(name)
            if request(name, "/api/touch", token) != touch or request(name, "/api/outbounds", token) != outbounds:
                raise RuntimeError("SQLite restart did not preserve API data")
            stop(name)
            archive = Path(temp) / "latest.json"
            latest = cli("runtime-export", directory, "--out", str(archive))["manifest"]
            rollback = cli("runtime-rollback", directory)["runtime"]
            if rollback["manifest"] != latest or Path(rollback["database"]).name == "bolt.db":
                raise RuntimeError("rollback did not restore latest SQLite data into a new Bolt")
            for target in (directory, restored):
                if target == restored:
                    restored_result = cli("runtime-restore", target, "--in", str(archive))["runtime"]
                    if restored_result["manifest"] != latest:
                        raise RuntimeError("restored SQLite snapshot differs")
                name = start(target)
                token = login(name)
                if request(name, "/api/touch", token) != touch or request(name, "/api/outbounds", token) != outbounds:
                    raise RuntimeError("restored service API data differs")
                stop(name)
            print(json.dumps({"success": True, "checks": ["fresh-defaults", "account-login", "node-import",
                "unicode-outbound", "maintenance-lock", "sqlite-restart", "latest-write-bolt-rollback",
                "archive-sqlite-restore"], "hostNetwork": False, "hostPorts": False, "dockerSocket": False}))
        finally:
            for name in containers:
                command(["docker", "rm", "-f", name], required=False)


if __name__ == "__main__":
    main()
