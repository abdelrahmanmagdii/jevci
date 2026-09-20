import json
import shlex
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

    def test_memory_limits_are_profile_specific_with_swap_disabled(self):
        expected = {"prometheus": 12, "caddy": 6, "jaeger": 6}
        self.assertEqual(set(qualify.PROFILES), set(expected))
        for name, limit in expected.items():
            with self.subTest(profile=name):
                profile = qualify.PROFILES[name]
                self.assertEqual(profile["memory_limit_gib"], limit)
                command = qualify.container_command("test-worker", "test-image", self.root, name, profile)
                self.assertEqual(command[command.index("--memory") + 1], f"{limit}g")
                self.assertEqual(command[command.index("--memory-swap") + 1], f"{limit}g")
                self.assertEqual(command[command.index("--cpus") + 1], "2")
                self.assertEqual(command[command.index("--pids-limit") + 1], "2048")
        self.assertEqual(qualify.PROFILE_SECONDS, 1800)

    def test_run_metadata_records_limits_for_selected_profiles(self):
        limits = {"prometheus": 12, "caddy": 6, "jaeger": 6}
        for selection in ("all", *limits):
            with self.subTest(profile=selection):
                output = self.root / selection
                names = list(limits) if selection == "all" else [selection]

                def execute(name, *_args):
                    return {"profile": name, "configuration": qualify.PROFILES[name], "status": "qualified", "elapsed_seconds": 1}

                with mock.patch.dict(qualify.os.environ, {"GITHUB_ACTIONS": "true"}, clear=True), \
                     mock.patch.object(qualify.platform, "system", return_value="Linux"), \
                     mock.patch.object(qualify.platform, "machine", return_value="x86_64"), \
                     mock.patch.object(qualify.os, "getuid", return_value=1001), \
                     mock.patch.object(qualify.os, "getgid", return_value=1001), \
                     mock.patch.object(qualify, "host_resources", return_value={}), \
                     mock.patch.object(qualify, "qualify_profile", side_effect=execute) as run:
                    self.assertEqual(qualify.main(["--profile", selection, "--output", str(output)]), 0)
                result = json.loads((output / "run.json").read_text())
                self.assertEqual(result["version"], 2)
                self.assertEqual(result["container_memory_limits_gib"], {name: limits[name] for name in names})
                self.assertNotIn("container_memory_limit_gib", result)
                self.assertEqual(result["container_cpu_limit"], 2)
                self.assertEqual(result["per_profile_timeout_seconds"], 1800)
                self.assertEqual([call.args[0] for call in run.call_args_list], names)
                for profile in result["results"]:
                    self.assertEqual(profile["configuration"]["memory_limit_gib"], limits[profile["profile"]])

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

    def memory_snapshot(self, stage, peak=1024, events="oom 0\noom_kill 0\n"):
        directory = self.root / f"memory-{stage}"
        directory.mkdir(exist_ok=True)
        values = {
            "memory.current": "128\n", "memory.peak": f"{peak}\n",
            "memory.max": "6442450944\n", "memory.events": events,
            "memory.events.local": events,
        }
        for name, value in values.items():
            (directory / name).write_text(value)
        return directory

    def test_memory_peak_includes_setup_and_events_use_test_window(self):
        self.memory_snapshot("start", 1024)
        self.memory_snapshot("before-tests", 2048, "oom 2\noom_kill 1\n")
        self.memory_snapshot("after-tests", 4096, "oom 5\noom_kill 2\n")
        self.memory_snapshot("exit", 8192, "oom 5\noom_kill 2\n")
        result = qualify.memory_diagnostics(self.root)
        self.assertEqual(result["observed_peak_bytes"], 8192)
        self.assertEqual(result["snapshots"]["before-tests"]["memory.max"], 6442450944)
        self.assertEqual(result["test_event_deltas"]["memory.events"], {"oom": 3, "oom_kill": 1})
        self.assertEqual(result["test_event_deltas"]["memory.events.local"], {"oom": 3, "oom_kill": 1})
        self.assertEqual(result["errors"], [])

    def test_memory_counters_can_be_zero_or_unlimited_without_being_missing(self):
        for stage in ("start", "before-tests", "after-tests", "exit"):
            directory = self.memory_snapshot(stage, 0)
            (directory / "memory.max").write_text("max\n")
        result = qualify.memory_diagnostics(self.root)
        self.assertEqual(result["observed_peak_bytes"], 0)
        self.assertEqual(result["snapshots"]["exit"]["memory.max"], "max")
        self.assertEqual(result["test_event_deltas"]["memory.events"]["oom_kill"], 0)
        self.assertEqual(result["errors"], [])

    def test_missing_memory_counters_are_unknown_not_zero(self):
        result = qualify.memory_diagnostics(self.root)
        self.assertIsNone(result["observed_peak_bytes"])
        self.assertIsNone(result["test_event_deltas"]["memory.events"])
        self.assertIsNone(result["snapshots"]["exit"]["memory.current"])
        self.assertTrue(result["errors"])

    def test_missing_final_snapshot_does_not_imply_zero_test_oom_events(self):
        self.memory_snapshot("start", 1024)
        self.memory_snapshot("before-tests", 2048)
        result = qualify.memory_diagnostics(self.root)
        self.assertEqual(result["observed_peak_bytes"], 2048)
        self.assertIsNone(result["test_event_deltas"]["memory.events"])
        self.assertIsNone(result["snapshots"]["after-tests"]["memory.peak"])

    def test_invalid_memory_counters_are_reported_without_raising(self):
        directory = self.memory_snapshot("exit")
        for value in ("-1", "NaN", "Infinity", "12.5", "1_000", "18446744073709551616", ""):
            with self.subTest(peak=value):
                (directory / "memory.peak").write_text(value)
                result = qualify.memory_diagnostics(self.root)
                self.assertIsNone(result["observed_peak_bytes"])
                self.assertTrue(any(error.startswith("exit/memory.peak:") for error in result["errors"]))
        for value in ("oom -1", "oom nan", "oom 1\noom 2", "oom", ""):
            with self.subTest(events=value):
                (directory / "memory.events").write_text(value)
                result = qualify.memory_diagnostics(self.root)
                self.assertIsNone(result["snapshots"]["exit"]["memory.events"])

    def test_regressing_or_mismatched_event_counters_have_no_delta(self):
        self.memory_snapshot("before-tests", events="oom 2\noom_kill 1\n")
        for events in ("oom 1\noom_kill 1\n", "oom 3\n"):
            with self.subTest(events=events):
                self.memory_snapshot("after-tests", events=events)
                result = qualify.memory_diagnostics(self.root)
                self.assertIsNone(result["test_event_deltas"]["memory.events"])
                self.assertTrue(any("inconsistent counters" in error for error in result["errors"]))

    def test_memory_artifact_symlinks_are_not_read(self):
        directory = self.root / "memory-exit"
        directory.mkdir()
        target = self.root / "outside"
        target.write_text("999\n")
        (directory / "memory.peak").symlink_to(target)
        result = qualify.memory_diagnostics(self.root)
        self.assertIsNone(result["observed_peak_bytes"])
        self.assertEqual(target.read_text(), "999\n")

    def test_memory_diagnostics_survive_failed_timed_out_and_cancelled_profiles(self):
        for failure in (None, subprocess.TimeoutExpired("docker", 1), KeyboardInterrupt()):
            with self.subTest(failure=type(failure).__name__):
                directory = self.root / f"run-{type(failure).__name__}"
                diagnostic = {"observed_peak_bytes": 8192, "test_event_deltas": {"memory.events": {"oom_kill": 1}}}

                def execute(arguments, *_args, **_kwargs):
                    if arguments[1] == "pull" and failure is not None:
                        raise failure
                    stdout = '"sha256:fixture"' if arguments[1:3] == ["image", "inspect"] else ""
                    return subprocess.CompletedProcess(arguments, 1 if arguments[1] == "run" else 0, stdout=stdout)

                with mock.patch.object(qualify, "run_command", side_effect=execute), \
                     mock.patch.object(qualify, "evaluate_artifacts", return_value={"status": "test_failed"}), \
                     mock.patch.object(qualify, "memory_diagnostics", return_value=diagnostic) as collect:
                    result = qualify.qualify_profile("caddy", directory, 1001, 1001)
                expected = "test_failed" if failure is None else "cancelled" if isinstance(failure, KeyboardInterrupt) else "timeout"
                self.assertEqual(result["status"], expected)
                collect.assert_called_once_with(directory)
                self.assertEqual(result["memory_diagnostics"], diagnostic)
                self.assertEqual(json.loads((directory / "result.json").read_text())["memory_diagnostics"], diagnostic)

    def test_summary_distinguishes_missing_memory_diagnostics_from_zero(self):
        results = [
            {"profile": "missing", "status": "timeout", "elapsed_seconds": 1},
            {"profile": "measured", "status": "qualified", "elapsed_seconds": 2,
             "memory_diagnostics": {"observed_peak_bytes": 3 * 1024 ** 3, "test_event_deltas": {"memory.events": {"oom_kill": 0}}}},
        ]
        qualify.write_run(self.root, {}, results)
        summary = (self.root / "summary.md").read_text()
        self.assertIn("| missing | timeout | 1 | n/a | n/a |", summary)
        self.assertIn("| measured | qualified | 2 | 3.000 | 0 |", summary)
        self.assertEqual(json.loads((self.root / "run.json").read_text())["results"], results)

    def test_worker_captures_memory_without_changing_exit_status(self):
        worker = (qualify.CONTEXT / "worker.sh").read_text()
        for test_exit, setup_exit, private_cgroup in ((0, 0, True), (7, 0, True), (7, 0, False), (0, 3, True)):
            with self.subTest(test_exit=test_exit, setup_exit=setup_exit, private_cgroup=private_cgroup):
                root = self.root / f"worker-{test_exit}-{setup_exit}-{private_cgroup}"
                output, source, cgroup = (root / name for name in ("output", "source", "cgroup"))
                for directory in (output, source / ".git", cgroup):
                    directory.mkdir(parents=True)
                membership = root / "membership"
                membership.write_text("0::/\n" if private_cgroup else "0::/host-path\n")
                for name, value in {"cgroup.controllers": "memory", "memory.current": "128", "memory.peak": "1024", "memory.max": "6442450944", "memory.events": "oom 0\noom_kill 0\n", "memory.events.local": "oom 0\noom_kill 0\n"}.items():
                    (cgroup / name).write_text(value)
                script = worker
                for name, old, new in (("out", "/output", output), ("src", "/work/source", source), ("cgroup_root", "/sys/fs/cgroup", cgroup), ("cgroup_membership", "/proc/self/cgroup", membership)):
                    script = script.replace(f"{name}={old}\n", f"{name}={shlex.quote(str(new))}\n")
                stubs = f"""
git() {{
  if [[ "$*" == *rev-parse* ]]; then printf '%s\\n' fixture; fi
  return 0
}}
go() {{
  case "$*" in
    'version') printf '%s\\n' 'go version go1.26.6 linux/amd64' ;;
    'env GOVERSION') printf '%s\\n' go1.26.6 ;;
    'env GOOS') printf '%s\\n' linux ;;
    'env GOARCH') printf '%s\\n' amd64 ;;
    'env -json '*|'list '*) printf '%s\\n' '{{}}' ;;
    'mod download') return {setup_exit} ;;
    'test '*)
      printf '%s\\n' 2048 > "$cgroup_root/memory.peak"
      printf 'oom 1\\noom_kill 1\\n' > "$cgroup_root/memory.events"
      return {test_exit}
      ;;
    *) return 99 ;;
  esac
}}
"""
                result = subprocess.run(["bash", "-c", stubs + script, "worker", "fixture", "example/repo", "fixture", "go1.26.6", "-mod=readonly", "10m", "0", "0", "", "0"], capture_output=True, text=True, timeout=10)
                expected_exit = setup_exit or test_exit
                self.assertEqual(result.returncode, expected_exit, result.stderr)
                self.assertEqual((output / "worker-exit.txt").read_text().strip(), str(expected_exit))
                self.assertTrue((output / "memory-start" / "cgroup.txt").exists())
                self.assertTrue((output / "memory-exit" / "cgroup.txt").exists())
                diagnostic = qualify.memory_diagnostics(output)
                if setup_exit:
                    self.assertFalse((output / "test-exit.txt").exists())
                    self.assertIsNone(diagnostic["test_event_deltas"]["memory.events"])
                elif private_cgroup:
                    self.assertEqual((output / "test-exit.txt").read_text().strip(), str(test_exit))
                    self.assertEqual(diagnostic["observed_peak_bytes"], 2048)
                    self.assertEqual(diagnostic["test_event_deltas"]["memory.events"]["oom_kill"], 1)
                    self.assertEqual(diagnostic["test_event_deltas"]["memory.events.local"]["oom_kill"], 0)
                    self.assertEqual(diagnostic["errors"], [])
                else:
                    self.assertTrue((output / "memory-exit" / "unavailable.txt").exists())
                    self.assertIsNone(diagnostic["observed_peak_bytes"])

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
