import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("compose_smoke", Path(__file__).with_name("compose_smoke.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class ComposeSmokePreflightTests(unittest.TestCase):
    def test_missing_docker_never_starts_resources(self):
        with patch("sys.argv", ["compose_smoke.py"]), patch.object(smoke.shutil, "which", return_value=None), patch.object(smoke.subprocess, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "verification NOT performed"):
                smoke.main()
            run.assert_not_called()

    def test_unavailable_daemon_never_starts_compose(self):
        with patch("sys.argv", ["compose_smoke.py"]), patch.object(smoke.shutil, "which", return_value="docker"), patch.object(smoke.subprocess, "run", side_effect=subprocess.CalledProcessError(1, ["docker", "info"])) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                smoke.main()
            self.assertEqual(run.call_count, 1)
            self.assertEqual(run.call_args.args[0], ["docker", "info"])

    def test_restart_waits_for_database_before_applications(self):
        events = []
        probes = iter([subprocess.CalledProcessError(1, ["psql"]), "1"])

        def compose(*args):
            events.append(args)

        def sql(statement):
            events.append(("probe", statement))
            result = next(probes)
            if isinstance(result, Exception):
                raise result
            return result

        with patch.object(smoke.time, "sleep"):
            smoke.restart_services(compose, sql, lambda: events.append(("ready",)))
        self.assertEqual(events, [("restart", "postgres"), ("probe", "SELECT 1"),
                                  ("probe", "SELECT 1"), ("restart", "control", "node"), ("ready",)])

    def test_database_timeout_does_not_restart_applications(self):
        events = []
        with patch.object(smoke.time, "monotonic", side_effect=[0, 91]):
            with self.assertRaisesRegex(RuntimeError, "Database did not recover"):
                smoke.restart_services(lambda *args: events.append(args), lambda _: "0", lambda: None)
        self.assertEqual(events, [("restart", "postgres")])

    def test_fault_kill_only_targets_generated_project_node(self):
        events = []
        smoke.kill_test_node(lambda *args: events.append(args), "vds-smoke-0123456789ab")
        self.assertEqual(events, [("kill", "-s", "SIGKILL", "node")])
        for project in ["production", "vds-smoke-../../unsafe", "vds-smoke-zzzzzzzzzzzz"]:
            with self.assertRaisesRegex(RuntimeError, "Refusing fault injection"):
                smoke.kill_test_node(lambda *args: events.append(args), project)
        self.assertEqual(len(events), 1)

    def test_state_wait_timeout_is_bounded(self):
        with patch.object(smoke.time, "monotonic", side_effect=[0, 16]):
            with self.assertRaisesRegex(RuntimeError, "Timed out: streaming"):
                smoke.wait_for(lambda: False, "streaming")

    def test_report_redacts_credentials(self):
        self.assertEqual(smoke.redact_report("password token key", ["password", "token", "key", ""]),
                         "[REDACTED] [REDACTED] [REDACTED]")

    def test_fragmented_headers_count_only_body(self):
        client = Mock()
        client.recv.side_effect = [b"HTTP/1.", b"1 200 OK\r\nContent-Len", b"gth: 8\r\n\r\nabc"]
        self.assertEqual(smoke.read_transfer_headers(client, 8), 3)

    def test_invalid_transfer_headers_rejected(self):
        for data in [b"HTTP/1.1 500 Error\r\nContent-Length: 8\r\n\r\n",
                     b"HTTP/1.1 200 OK\r\nContent-Length: 9\r\n\r\n",
                     b"HTTP/1.1 200 OK\r\nContent-Length: 8\r\nContent-Length: 8\r\n\r\n"]:
            client = Mock()
            client.recv.return_value = data
            with self.assertRaises(RuntimeError):
                smoke.read_transfer_headers(client, 8)

    def test_interrupted_body_must_be_incomplete(self):
        client = Mock()
        client.recv.side_effect = [b"ab", b""]
        self.assertEqual(smoke.drain_interrupted_transfer(client, 1, 8), 3)
        client.recv.side_effect = [b"1234567"]
        with self.assertRaisesRegex(RuntimeError, "not interrupted"):
            smoke.drain_interrupted_transfer(client, 1, 8)

    def test_killed_connection_timeout_never_passes(self):
        client = Mock()
        client.recv.side_effect = smoke.socket.timeout()
        with self.assertRaisesRegex(RuntimeError, "did not terminate"):
            smoke.drain_interrupted_transfer(client, 0, 8)

    def test_fault_flow_checks_exclusion_before_kill_and_recovers(self):
        events = []
        content = b"fixture"
        client = Mock()
        client.__enter__ = Mock(return_value=client)
        client.__exit__ = Mock(return_value=False)
        states = iter([{"status": "streaming", "retry_count": 1},
                       {"status": "streaming", "retry_count": 1},
                       {"status": "completed", "retry_count": 2},
                       {"status": "completed", "retry_count": 2}])

        def request(path, *args, **kwargs):
            events.append(("request", path, kwargs.get("expected", 200)))
            if path == "/api/v1/claims":
                return {"claim_id": "clm_test", "download": {"url": "http://127.0.0.1:8001/d/test"}}
            if path.startswith("/api/v1/claims/"):
                return next(states)
            return content

        with patch.object(smoke.secrets, "token_bytes", return_value=content), \
             patch.object(smoke, "upload_fixture"), \
             patch.object(smoke.socket, "socket", return_value=client), \
             patch.object(smoke, "read_transfer_headers", return_value=0), \
             patch.object(smoke, "drain_interrupted_transfer", return_value=1), \
             patch("builtins.print") as output:
            smoke.fault_download(lambda *args: events.append(args), "vds-smoke-0123456789ab",
                                 request, lambda: events.append(("ready",)),
                                 "http://127.0.0.1:8001", "admin-token", "api-key", 1)
        exclusion = events.index(("request", "/d/test", 409))
        kill = events.index(("kill", "-s", "SIGKILL", "node"))
        restart = events.index(("up", "-d", "--no-deps", "node"))
        self.assertLess(exclusion, kill)
        self.assertLess(kill, restart)
        self.assertEqual(events[-1], ("request", "/d/test", 410))
        self.assertIn("FAULT PASS", output.call_args.args[0])

    def test_port_allocator_returns_valid_port(self):
        self.assertTrue(0 < smoke.free_port() < 65536)


if __name__ == "__main__":
    unittest.main()
