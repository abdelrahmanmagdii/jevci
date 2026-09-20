import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import qualify


class QualificationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.profile = qualify.PROFILES["caddy"]
        self.package = self.profile["module"] + "/example"

    def event_stream(self, action="pass", terminal=True):
        events = [
            {"Action": "run", "Package": self.package, "Test": "TestOne"},
            {"Action": action, "Package": self.package, "Test": "TestOne"},
        ]
        if terminal:
            events.append({"Action": action, "Package": self.package})
        return "\n".join(json.dumps(event) for event in events)

    def artifacts(self):
        values = {
            "test-exit.txt": "0\n",
            "worker-exit.txt": "0\n",
            "checkout.txt": self.profile["revision"],
            "storage.txt": "",
            "tracked-changes.txt": "",
            "test-wall-seconds.txt": "0.125\n",
            "go-test.jsonl": self.event_stream(),
            "packages.json": json.dumps({
                "ImportPath": self.package,
                "Module": {"Path": self.profile["module"]},
                "TestGoFiles": ["example_test.go"],
            }),
            "go-env.json": json.dumps({
                "GOVERSION": self.profile["go_version"],
                "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1",
                "GOFLAGS": self.profile["go_flags"], "GOTOOLCHAIN": "local",
            }),
        }
        for name, value in values.items():
            (self.root / name).write_text(value, encoding="utf-8")

    def test_complete_profile_qualifies(self):
        self.artifacts()
        result = qualify.evaluate_artifacts(self.root, self.profile, 0)
        self.assertEqual(result["status"], "qualified")
        self.assertEqual(result["declared_test_targets"], 1)
        self.assertEqual(result["test_wall_seconds"], 0.125)

    def test_incomplete_output_is_not_success(self):
        self.artifacts()
        (self.root / "go-test.jsonl").write_text(self.event_stream(terminal=False))
        result = qualify.evaluate_artifacts(self.root, self.profile, 0)
        self.assertEqual(result["status"], "incomplete")
        self.assertEqual(result["missing_targets"], [self.package])

    def test_skips_are_not_success(self):
        self.artifacts()
        (self.root / "go-test.jsonl").write_text(self.event_stream("skip"))
        result = qualify.evaluate_artifacts(self.root, self.profile, 0)
        self.assertEqual(result["status"], "incomplete")

    def test_test_failure_is_preserved(self):
        self.artifacts()
        (self.root / "go-test.jsonl").write_text(self.event_stream("fail"))
        (self.root / "test-exit.txt").write_text("1")
        (self.root / "worker-exit.txt").write_text("1")
        result = qualify.evaluate_artifacts(self.root, self.profile, 1)
        self.assertEqual(result["status"], "test_failed")
        self.assertEqual(result["failed_packages"], [self.package])

    def test_source_drift_is_not_success(self):
        self.artifacts()
        (self.root / "tracked-changes.txt").write_text("go.mod\n")
        result = qualify.evaluate_artifacts(self.root, self.profile, 0)
        self.assertEqual(result["status"], "source_modified")

    def test_missing_test_execution_is_setup_failure(self):
        result = qualify.evaluate_artifacts(self.root, self.profile, 1)
        self.assertEqual(result["status"], "setup_failed")

    def test_build_context_and_revision_are_checked(self):
        self.artifacts()
        (self.root / "checkout.txt").write_text("wrong-revision")
        with self.assertRaises(ValueError):
            qualify.evaluate_artifacts(self.root, self.profile, 0)
        self.artifacts()
        context = json.loads((self.root / "go-env.json").read_text())
        context["GOFLAGS"] = ""
        (self.root / "go-env.json").write_text(json.dumps(context))
        with self.assertRaises(ValueError):
            qualify.evaluate_artifacts(self.root, self.profile, 0)

    def test_timing_must_be_finite_and_nonnegative(self):
        self.artifacts()
        for value in ("NaN", "Infinity", "-1"):
            (self.root / "test-wall-seconds.txt").write_text(value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                qualify.evaluate_artifacts(self.root, self.profile, 0)

    def test_discovery_rejects_invalid_module_and_empty_targets(self):
        for package in (
            {"ImportPath": self.package, "Module": None, "TestGoFiles": ["a_test.go"]},
            {"ImportPath": self.package, "Module": {"Path": "other"}, "TestGoFiles": ["a_test.go"]},
            {"ImportPath": self.package, "Module": {"Path": self.profile["module"]}},
        ):
            with self.subTest(package=package), self.assertRaises(ValueError):
                qualify.test_targets(json.dumps(package), self.profile["module"])
        with self.assertRaises(ValueError):
            qualify.test_targets("", self.profile["module"])

    def test_malformed_and_duplicate_events_are_rejected(self):
        terminal = json.dumps({"Action": "pass", "Package": self.package})
        for stream in ("{bad json}", "[]", terminal + "\n" + terminal):
            with self.subTest(stream=stream), self.assertRaises(ValueError):
                qualify.test_summary(stream, {self.package})

    def test_container_has_only_output_mount_and_no_credential_environment(self):
        command = qualify.container_command("test-worker", "test-image", self.root, "caddy", self.profile)
        self.assertEqual(command.count("--mount"), 1)
        self.assertIn(f"type=bind,src={self.root},dst=/output", command)
        self.assertNotIn("--env", command)
        self.assertNotIn("--env-file", command)
        self.assertNotIn("/var/run/docker.sock", " ".join(command))
        self.assertNotIn("--ulimit", command)
        self.assertIn("no-new-privileges=true", command)
        dockerfile = (qualify.CONTEXT / "Dockerfile").read_text()
        entrypoint = next(line for line in dockerfile.splitlines() if line.startswith("ENTRYPOINT "))
        self.assertEqual(json.loads(entrypoint[len("ENTRYPOINT "):])[:2], ["/usr/bin/env", "-i"])

    def test_artifact_links_are_removed_without_touching_targets(self):
        outside = self.root / "outside"
        outside.write_text("preserved")
        artifacts = self.root / "artifacts"
        artifacts.mkdir()
        (artifacts / "go-test.jsonl").symlink_to(outside)
        self.assertEqual(qualify.remove_artifact_links(artifacts), ["go-test.jsonl"])
        self.assertEqual(outside.read_text(), "preserved")

    def test_profile_timeout_stops_only_its_worker(self):
        commands = []

        def execute(arguments, *_args, **_kwargs):
            commands.append(arguments)
            if arguments[1] == "pull":
                raise subprocess.TimeoutExpired(arguments, 1)
            return subprocess.CompletedProcess(arguments, 0, stdout="")

        with mock.patch.object(qualify, "run_command", side_effect=execute):
            result = qualify.qualify_profile("caddy", self.root / "run", 1001, 1001)
        self.assertEqual(result["status"], "timeout")
        stops = [args for args in commands if args[1] == "stop"]
        self.assertEqual(len(stops), 1)
        self.assertTrue(stops[0][-1].startswith("jevci-qualify-caddy-"))
        self.assertTrue((self.root / "run" / "result.json").is_file())

    def test_cancelled_profile_still_stops_its_worker(self):
        commands = []
        def execute(arguments, *_args, **_kwargs):
            commands.append(arguments)
            if arguments[1] == "pull":
                raise KeyboardInterrupt
            return subprocess.CompletedProcess(arguments, 0, stdout="")
        with mock.patch.object(qualify, "run_command", side_effect=execute):
            result = qualify.qualify_profile("caddy", self.root / "cancelled", 1001, 1001)
        self.assertEqual(result["status"], "cancelled")
        self.assertTrue(result["cancelled"])
        self.assertTrue(any(args[1] == "stop" for args in commands))

    def test_cleanup_failure_is_reported(self):
        def execute(arguments, *_args, **_kwargs):
            if arguments[1] == "pull":
                raise subprocess.TimeoutExpired(arguments, 1)
            return subprocess.CompletedProcess(arguments, 0, stdout="still-running" if arguments[1] == "ps" else "")
        with mock.patch.object(qualify, "run_command", side_effect=execute):
            result = qualify.qualify_profile("caddy", self.root / "cleanup", 1001, 1001)
        self.assertEqual(result["status"], "cleanup_failed")

    def test_host_resource_metadata(self):
        with mock.patch.object(qualify.os, "sysconf", side_effect=[4096, 2097152]), mock.patch.object(Path, "read_text", return_value="model name\t: Example CPU\n"):
            result = qualify.host_resources()
        self.assertEqual(result["host_memory_bytes"], 8589934592)
        self.assertEqual(result["host_cpu_model"], "Example CPU")

    def test_profiles_use_fixed_revisions_and_image_digests(self):
        self.assertEqual(set(qualify.PROFILES), {"prometheus", "caddy", "jaeger"})
        for profile in qualify.PROFILES.values():
            self.assertRegex(profile["revision"], r"^[0-9a-f]{40}$")
            self.assertRegex(profile["image"], r"@sha256:[0-9a-f]{64}$")


if __name__ == "__main__":
    unittest.main()
