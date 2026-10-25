import tempfile
import unittest
from pathlib import Path
from unittest import mock

import test_metrics


class TestMetricsTest(unittest.TestCase):
    def run_main(self, body: str) -> int:
        with tempfile.TemporaryDirectory() as raw_directory:
            path = Path(raw_directory) / "events.jsonl"
            path.write_text(body, encoding="utf-8")
            with mock.patch("sys.argv", ["test_metrics.py", str(path)]):
                return test_metrics.main()

    def test_rejects_empty_or_non_test_evidence(self):
        for body in ("", "{}\n", '{"Action":"pass","Package":"example"}\n'):
            with self.subTest(body=body):
                self.assertEqual(1, self.run_main(body))

    def test_accepts_final_test_case_event(self):
        event = (
            '{"Time":"2026-08-31T00:00:00Z","Action":"pass",'
            '"Package":"example","Test":"TestReal","Elapsed":0.1}\n'
        )
        self.assertEqual(0, self.run_main(event))


if __name__ == "__main__":
    unittest.main()
