import json
import os
import pathlib
import tempfile
import unittest

import processes


@unittest.skipUnless(os.name == "posix", "Linux Guest /proc fixture")
class ProcessSnapshotTest(unittest.TestCase):
    def fixture(self, root, pid, name="strange ) name", ticks=300):
        directory = root / str(pid)
        directory.mkdir()
        fields = ["0"] * 20
        fields[0], fields[19] = "S", str(ticks)
        (directory / "stat").write_text(f"{pid} ({name}) " + " ".join(fields))
        (directory / "exe").symlink_to("/usr/bin/node")
        (directory / "cmdline").write_bytes(b"node\0--api-key=DO_NOT_READ_SECRET\0")
        (directory / "environ").write_bytes(b"KEY=DO_NOT_READ_SECRET\0")

    def test_process_identity_start_time_and_no_arguments(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / "stat").write_text("cpu 0 0 0\nbtime 1000\n")
            self.fixture(root, 900002)
            (root / "900003").mkdir()  # exited/inaccessible process ignored
            self.fixture(root, os.getpid())  # collector excluded
            value = processes.snapshot(root)
            self.assertEqual(len(value["processes"]), 1)
            process = value["processes"][0]
            self.assertEqual(process["pid"], 900002)
            self.assertEqual(process["start_time"], 1000 + 300 // os.sysconf("SC_CLK_TCK"))
            self.assertEqual(process["cmdline"], "/usr/bin/node")
            self.assertNotIn("DO_NOT_READ_SECRET", json.dumps(value))
            self.assertGreater(value["processes_collected_at"], 0)
            with self.assertRaises(ValueError):
                processes.snapshot(root, limit=0)

    def test_missing_boot_time_is_not_a_successful_empty_sample(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / "stat").write_text("cpu 0 0 0\n")
            with self.assertRaises(ValueError):
                processes.snapshot(root)


if __name__ == "__main__":
    unittest.main()
