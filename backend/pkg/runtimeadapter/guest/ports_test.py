"""Regression coverage for proc TCP discovery in a Docker Guest."""
import pathlib
import tempfile
import unittest
from unittest.mock import patch

from ports import discover_ports


def listener(address, port, inode, state="0A"):
    return f"0: {address}:{port:04X} 00000000:0000 {state} 0:0 0:0 0 0 0 {inode}\n"


class PortDiscoveryTests(unittest.TestCase):
    def discover(self, tcp, tcp6=""):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "net").mkdir()
            (root / "net/tcp").write_text("header\n" + tcp, encoding="ascii")
            (root / "net/tcp6").write_text("header\n" + tcp6, encoding="ascii")
            (root / "42/fd").mkdir(parents=True)
            (root / "42/comm").write_text("python3\n", encoding="utf-8")
            (root / "42/fd/1").touch()
            with patch("ports.os.readlink", return_value="socket:[2]"):
                return discover_ports(str(root))

    def test_dns_only_environment_has_no_preview_ports(self):
        self.assertEqual(self.discover(listener("0B00007F", 36103, 1)), [])

    def test_keeps_development_listeners_and_excludes_dns_and_connections(self):
        result = self.discover(
            listener("0B00007F", 36103, 1) +
            listener("0100007F", 8080, 2) +
            listener("00000000", 3000, 3) +
            listener("0100007F", 9999, 4, state="01"),
            listener("00000000000000000000000001000000", 8081, 5) +
            listener("0000000000000000FFFF00000B00007F", 36104, 6))
        self.assertEqual([item["port"] for item in result], [3000, 8080, 8081])
        self.assertEqual(result[1]["process"], "python3")
        self.assertEqual(result[0]["process"], "")

    def test_does_not_filter_application_by_dns_port_number(self):
        result = self.discover(listener("0B00007F", 36103, 1) +
                               listener("0100007F", 36103, 2))
        self.assertEqual(result, [{"port": 36103, "process": "python3"}])


if __name__ == "__main__":
    unittest.main()
