import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def restart_services(compose, sql, ready):
    compose("restart", "postgres")
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            if sql("SELECT 1") == "1":
                break
        except (subprocess.SubprocessError, OSError):
            pass
        time.sleep(1)
    else:
        raise RuntimeError("Database did not recover after restart")
    compose("restart", "control", "node")
    ready()


def redact_report(text, values):
    for value in values:
        if value:
            text = text.replace(value, "[REDACTED]")
    return text


def wait_for(predicate, description, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.1)
    raise RuntimeError("Timed out: " + description)


def kill_test_node(compose, project):
    if not project.startswith("vds-smoke-") or len(project) != 22 or any(c not in "0123456789abcdef" for c in project[10:]):
        raise RuntimeError("Refusing fault injection outside generated smoke project")
    compose("kill", "-s", "SIGKILL", "node")


def upload_fixture(request, token, category_id, content):
    boundary = "vds-" + secrets.token_hex(12)
    fields = b""
    for name, value in [("category_id", str(category_id)), ("node_id", "1")]:
        fields += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n").encode()
    fields += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"smoke.mp4\"\r\nContent-Type: video/mp4\r\n\r\n").encode() + content + (f"\r\n--{boundary}--\r\n").encode()
    video = request("/admin/videos", fields, token, expected=201, content_type="multipart/form-data; boundary=" + boundary)
    if video["sha256"] != hashlib.sha256(content).hexdigest():
        raise RuntimeError("Upload digest mismatch")
    return video


def read_transfer_headers(client, expected_size):
    data = b""
    deadline = time.monotonic() + 10
    while b"\r\n\r\n" not in data:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError("Transfer headers timed out")
        client.settimeout(remaining)
        chunk = client.recv(4096)
        if not chunk:
            raise RuntimeError("Transfer closed before headers")
        data += chunk
        if len(data) > 65536:
            raise RuntimeError("Transfer headers too large")
    headers, body = data.split(b"\r\n\r\n", 1)
    lines = headers.split(b"\r\n")
    status = lines[0].split()
    if len(status) < 2 or status[0] not in (b"HTTP/1.0", b"HTTP/1.1") or status[1] != b"200":
        raise RuntimeError("Fault transfer did not start")
    lengths = [line.split(b":", 1)[1].strip() for line in lines[1:] if line.lower().startswith(b"content-length:")]
    if lengths != [str(expected_size).encode()]:
        raise RuntimeError("Fault transfer length mismatch")
    return len(body)


def drain_interrupted_transfer(client, received, expected_size):
    deadline = time.monotonic() + 15
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError("Killed transfer did not terminate")
        client.settimeout(remaining)
        try:
            chunk = client.recv(65536)
        except ConnectionResetError:
            break
        except socket.timeout as error:
            raise RuntimeError("Killed transfer did not terminate") from error
        if not chunk:
            break
        received += len(chunk)
        if received >= expected_size:
            raise RuntimeError("Fault fixture was not interrupted")
    if received >= expected_size:
        raise RuntimeError("Fault fixture was not interrupted")
    return received


def fault_download(compose, project, request, ready, node, token, api_key, category_id):
    content = secrets.token_bytes(16 * 1024 * 1024)
    upload_fixture(request, token, category_id, content)
    claim = request("/api/v1/claims", {"category_id": category_id}, api_key, expected=201,
                    headers={"Idempotency-Key": "compose-fault"})
    url = urllib.parse.urlsplit(claim["download"]["url"])
    target = urllib.parse.urlsplit(node)
    if url.netloc != target.netloc:
        raise RuntimeError("Fault URL outside isolated node")
    detail_path = "/api/v1/claims/" + claim["claim_id"]
    with socket.socket() as client:
        client.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4096)
        client.settimeout(10)
        client.connect((target.hostname, target.port))
        client.sendall(("GET " + url.path + " HTTP/1.1\r\nHost: " + target.netloc + "\r\nConnection: close\r\n\r\n").encode())
        received = read_transfer_headers(client, len(content))
        wait_for(lambda: request(detail_path, token=api_key)["status"] == "streaming", "active streaming")
        request(url.path, base=node, expected=409)
        kill_test_node(compose, project)
        drain_interrupted_transfer(client, received, len(content))
    detail = request(detail_path, token=api_key)
    if detail["status"] != "streaming" or detail["retry_count"] != 1:
        raise RuntimeError("Killed transfer incorrectly committed")
    compose("up", "-d", "--no-deps", "node")
    ready()
    if request(url.path, base=node) != content:
        raise RuntimeError("Recovered download content mismatch")
    wait_for(lambda: request(detail_path, token=api_key)["status"] == "completed", "recovered completion")
    if request(detail_path, token=api_key)["retry_count"] != 2:
        raise RuntimeError("Recovery did not increment attempt")
    request(url.path, base=node, expected=410)
    print("FAULT PASS: active exclusion/SIGKILL/truncated connection/restart/recovered bytes/attempt fencing")


def main():
    parser = argparse.ArgumentParser(description="Isolated Compose smoke test; never deletes volumes")
    parser.add_argument("--keep-running", action="store_true")
    parser.add_argument("--fault-tests", action="store_true")
    args = parser.parse_args()
    if not shutil.which("docker"):
        raise RuntimeError("Docker executable unavailable; container verification NOT performed")
    subprocess.run(["docker", "info"], check=True, stdout=subprocess.DEVNULL, timeout=30)
    subprocess.run(["docker", "compose", "version"], check=True, timeout=15)
    project = "vds-smoke-" + secrets.token_hex(6)
    env = os.environ.copy()
    ports = [free_port() for _ in range(3)]
    if len(set(ports)) != 3:
        raise RuntimeError("Port allocation collision; no resources started")
    env.update(VDS_DB_PASSWORD=secrets.token_hex(24), VDS_JWT_SECRET=secrets.token_hex(32),
               VDS_POSTGRES_PORT=str(ports[0]), VDS_CONTROL_PORT=str(ports[1]), VDS_NODE_PORT=str(ports[2]),
               VDS_ADMIN_USERNAME="smoke-admin", VDS_ADMIN_PASSWORD=secrets.token_hex(24))
    control = "http://127.0.0.1:" + str(ports[1])
    node = "http://127.0.0.1:" + str(ports[2])

    def compose(*arguments, capture=False, timeout=120):
        return subprocess.run(["docker", "compose", "-p", project, "-f", str(ROOT / "docker-compose.yml"), *arguments],
                              cwd=ROOT, env=env, check=True, capture_output=capture, text=True, timeout=timeout)

    def sql(statement):
        return compose("exec", "-T", "postgres", "psql", "-U", "video_distribution", "-d", "video_distribution",
                       "-v", "ON_ERROR_STOP=1", "-At", "-c", statement, capture=True).stdout.strip()

    def request(path, data=None, token=None, base=control, expected=200, content_type="application/json", headers=None):
        payload = json.dumps(data).encode() if isinstance(data, dict) else data
        req_headers = dict(headers or {})
        if payload is not None:
            req_headers["Content-Type"] = content_type
        if token:
            req_headers["Authorization"] = "Bearer " + token
        req = urllib.request.Request(base + path, data=payload, headers=req_headers)
        try:
            response = urllib.request.urlopen(req, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read()
            if response.status != expected:
                raise RuntimeError("Unexpected HTTP status for " + path.split("/d/")[0] + ": " + str(response.status))
            return json.loads(body) if "application/json" in response.headers.get("Content-Type", "") else body

    def ready():
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            try:
                request("/health/ready")
                request("/health/ready", base=node)
                return
            except (OSError, RuntimeError):
                time.sleep(1)
        raise RuntimeError("Services did not become ready")

    print("Isolated project:", project, flush=True)
    started = False
    try:
        compose("config", "--quiet")
        started = True
        compose("up", "-d", "--build", timeout=900)
        ready()
        compose("run", "--rm", "--no-deps", "-e", "VDS_ADMIN_USERNAME", "-e", "VDS_ADMIN_PASSWORD", "control", "bootstrap-admin")
        token = request("/admin/login", {"username": env["VDS_ADMIN_USERNAME"], "password": env["VDS_ADMIN_PASSWORD"]})["token"]
        user = request("/admin/users", {"internal_name": "compose-smoke", "daily_quota": 10}, token, expected=201)
        category = request("/admin/categories", {"name": "compose-smoke"}, token, expected=201)
        uid, cid = int(user["id"]), int(category["id"])
        request(f"/admin/users/{uid}/permissions/categories/{cid}", {}, token, expected=204)
        api_key = request("/admin/api_keys", {"user_id": uid}, token, expected=201)["api_key"]
        sql("UPDATE storage_nodes SET download_base_url='" + node + "' WHERE id=1")
        content = secrets.token_bytes(32768)
        video = upload_fixture(request, token, cid, content)
        claim = request("/api/v1/claims", {"category_id": cid}, api_key, expected=201, headers={"Idempotency-Key": "compose-smoke"})
        download_url = urllib.parse.urlsplit(claim["download"]["url"])
        if download_url.netloc != urllib.parse.urlsplit(node).netloc:
            raise RuntimeError("Download URL points outside isolated node")
        if request(download_url.path, base=node) != content:
            raise RuntimeError("Downloaded bytes differ")
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if request("/api/v1/claims/" + claim["claim_id"], token=api_key)["status"] == "completed":
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("Download completion not persisted")
        request(download_url.path, base=node, expected=410)
        restart_services(compose, sql, ready)
        if request("/api/v1/claims/" + claim["claim_id"], token=api_key)["status"] != "completed":
            raise RuntimeError("Claim did not survive restart")
        video_id = video["video_id"]
        if not video_id.startswith("vid_") or not video_id[4:].isalnum():
            raise RuntimeError("Invalid fixture video identifier")
        sql("UPDATE claims SET completed_at=NOW()-INTERVAL '2 hours' WHERE video_id=(SELECT id FROM videos WHERE video_id='" + video_id + "')")
        deadline = time.monotonic() + 150
        while time.monotonic() < deadline:
            if sql("SELECT status FROM videos WHERE video_id='" + video_id + "'") == "deleted":
                break
            time.sleep(2)
        else:
            raise RuntimeError("Cleanup did not delete uploaded fixture")
        compose("exec", "-T", "node", "test", "!", "-e", "/var/video-distribution/storage/" + video_id)
        if args.fault_tests:
            fault_download(compose, project, request, ready, node, token, api_key, cid)
        report = ROOT / "smoke-reports" / (project + ".json")
        report.parent.mkdir(exist_ok=True)
        report.write_text(json.dumps({"result": "PASS", "project": project, "fault_tests": args.fault_tests}), encoding="utf-8")
        print("PASS: build/start/bootstrap/upload/claim/download/repeat rejection/restart/physical cleanup")
    except Exception:
        if started:
            try:
                logs = compose("logs", "--no-color", "--tail", "200", capture=True)
                report = ROOT / "smoke-reports" / (project + ".log")
                report.parent.mkdir(exist_ok=True)
                text = logs.stdout
                text = redact_report(text, [env["VDS_DB_PASSWORD"], env["VDS_JWT_SECRET"], env["VDS_ADMIN_PASSWORD"],
                                            locals().get("token", ""), locals().get("api_key", "")])
                report.write_text(text, encoding="utf-8")
                print("Failure logs saved:", report)
            except Exception:
                print("Could not collect failure logs", file=sys.stderr)
        raise
    finally:
        if started:
            try:
                compose("ps")
            finally:
                if not args.keep_running:
                    compose("down", timeout=60)
                print("Volumes retained for isolated project:", project)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("FAIL:", str(error), file=sys.stderr)
        sys.exit(1)
