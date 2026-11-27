"""Run the real adapter against the Go kit when NIGHTDESK_BIN is provided.

The tools surface protocol failures; this small caller supplies the correction
and retry policy that a Hermes agent normally owns.
"""
from contextlib import contextmanager
import importlib.util
import json
import os
from pathlib import Path
import selectors
import subprocess
import tempfile
import time
import unittest
from unittest import mock
import urllib.request

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("conformance_plugin", HERE / "__init__.py")
plugin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plugin)
KEY = "synthetic-hermes-conformance-key"
DATE = "2026-10-30"


def edition(url="https://example.com/library"):
    return {"schema": 1, "lead": "news-1", "sections": [{"key": "news", "items": [
        {"id": "news-1", "kind": "news", "title": "Council opens a new library",
         "summary": "The new public library opened on Friday.",
         "source": "City Gazette", "url": url}]}]}


def edition_v2(url="https://example.com/library"):
    return {"schema": 2, "lead": "news-1", "sections": [{"key": "news", "items": [
        {"id": "news-1", "kind": "news", "title": "Council opens a new library",
         "summary": "The new public library opened on Friday.",
         "source": "City Gazette", "url": url, "byline": "Ana Ruiz",
         "sources": [{"outlet": "County Herald", "url": "https://herald.example/library",
                      "byline": "Lee Park"}]},
        {"id": "news-2", "kind": "opinion", "title": "The library is worth the wait",
         "summary": "A column on what the new branch changes for readers.",
         "source": "City Gazette Opinion", "url": "https://example.com/library-column",
         "byline": "Sam Hill", "about": "news-1",
         "stance": "The new branch was worth its cost."}]}]}


@unittest.skipUnless(os.environ.get("NIGHTDESK_BIN"), "NIGHTDESK_BIN is not set")
class ConformanceTests(unittest.TestCase):
    @contextmanager
    def kit(self, scenario):
        binary = str(Path(os.environ["NIGHTDESK_BIN"]).resolve())
        with tempfile.TemporaryDirectory() as directory:
            key_file = Path(directory) / "key"
            key_file.write_text(KEY + "\n", encoding="ascii")
            key_file.chmod(0o600)
            process = subprocess.Popen(
                [binary, "conformance", "serve", "--addr", "127.0.0.1:0",
                 "--scenario", scenario, "--key", str(key_file),
                 "--exit-on-finish", "--timeout", "10s"],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                with selectors.DefaultSelector() as selector:
                    selector.register(process.stdout, selectors.EVENT_READ)
                    self.assertTrue(selector.select(timeout=8), "kit did not start")
                line = process.stdout.readline().strip()
                prefix = "conformance listening at "
                self.assertTrue(line.startswith(prefix), "kit did not announce a listen address")
                base = line[len(prefix):]
                with mock.patch.dict(os.environ, {"NIGHTDESK_URL": base,
                                                 "NIGHTDESK_KEY_FILE": str(key_file)}):
                    yield base
                # Fetch the report only when finished: the final fetch requests
                # graceful exit, so the JSON remains available to the test.
                report = None
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    with urllib.request.urlopen(base + "/conformance/report", timeout=2) as response:
                        report = json.load(response)
                    if report["passed"]:
                        break
                    time.sleep(0.05)
                self.assertIsNotNone(report)
                self.assertTrue(report["passed"], report)
                self.assertEqual(report["scenario"], scenario)
                self.assertTrue(all(check["passed"] for check in report["checks"]), report)
                stdout, stderr = process.communicate(timeout=5)
                self.assertEqual(process.returncode, 0, stderr)
                self.assertNotIn(KEY, stdout + stderr)
            finally:
                if process.poll() is None:
                    process.terminate()
                try:
                    process.communicate(timeout=3)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.communicate()

    def test_validation(self):
        with self.kit("validation"):
            first = json.loads(plugin.nightdesk_publish_edition(
                {"date": DATE, "edition": edition("http://example.com/library")}))
            self.assertFalse(first["ok"])
            self.assertEqual(first["error"], "validation_failed")
            self.assertEqual(first["errors"], [
                {"path": "sections[0].items[0].url", "rule": "must be https"}])
            corrected = json.loads(plugin.nightdesk_publish_edition(
                {"date": DATE, "edition": edition()}))
            self.assertTrue(corrected["ok"], corrected)
            self.assertEqual(corrected["date"], DATE)

    def test_validation_schema_2(self):
        with self.kit("validation"):
            first = json.loads(plugin.nightdesk_publish_edition(
                {"date": DATE, "edition": edition_v2("http://example.com/library")}))
            self.assertEqual(first["error"], "validation_failed")
            # The kit validates the corrected edition with the server's own rules,
            # so bylines, further coverage and an opinion column must pass them.
            corrected = json.loads(plugin.nightdesk_publish_edition(
                {"date": DATE, "edition": edition_v2()}))
            self.assertTrue(corrected["ok"], corrected)
            self.assertEqual(corrected["date"], DATE)

    def test_retry(self):
        with self.kit("retry"):
            args = {"date": DATE, "edition": edition()}
            unavailable = json.loads(plugin.nightdesk_publish_edition(args))
            self.assertFalse(unavailable["ok"])
            self.assertEqual(unavailable["error"], "temporarily unavailable")
            time.sleep(0.15)
            limited = json.loads(plugin.nightdesk_publish_edition(args))
            self.assertFalse(limited["ok"])
            self.assertEqual(limited["error"], "rate limited")
            # The wrapper preserves JSON errors; the caller's policy waits at
            # least the kit's Retry-After: 1 before invoking the same safe PUT.
            time.sleep(1.05)
            published = json.loads(plugin.nightdesk_publish_edition(args))
            self.assertTrue(published["ok"], published)
            self.assertEqual(published["date"], DATE)

    def test_forbidden(self):
        with self.kit("forbidden"):
            forbidden = json.loads(plugin.nightdesk_publish_edition(
                {"date": DATE, "edition": edition()}))
            self.assertFalse(forbidden["ok"])
            self.assertEqual(forbidden["error"], "forbidden")
            # No retry; the kit observes the quiet period before passing.


if __name__ == "__main__":
    unittest.main()
