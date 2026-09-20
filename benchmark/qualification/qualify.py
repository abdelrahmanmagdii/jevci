import argparse
import datetime
import json
import math
import os
import platform
import signal
import stat
import subprocess
import sys
import time
import uuid
from pathlib import Path

PROFILES = {
    "prometheus": {
        "repo": "prometheus/prometheus",
        "revision": "9c8c131e849264f1dd210c89e5232dc9d1723536",
        "module": "github.com/prometheus/prometheus",
        "go_version": "go1.27.0",
        "image": "golang:1.27.0-bookworm@sha256:ba5ef6614ca131b80a635fc6a7b715d9ee8a7f333debdbb81afb68259c7d48d4",
        "go_flags": "-mod=readonly",
        "package_timeout": "10m",
        "short": False,
        "coverage": False,
        "storage": "",
        "hotrod": False,
    },
    "caddy": {
        "repo": "caddyserver/caddy",
        "revision": "62a72977e58c87fad7e7726c18b58f10c653f2d3",
        "module": "github.com/caddyserver/caddy/v2",
        "go_version": "go1.26.6",
        "image": "golang:1.26.6-bookworm@sha256:433f9dc4f8ea3a1ce4e28f9f15d0f7c056b10475307f886d6f1ac1ccc4abd976",
        "go_flags": "-mod=readonly -tags=nobadger,nomysql,nopgx",
        "package_timeout": "10m",
        "short": True,
        "coverage": True,
        "storage": "",
        "hotrod": False,
    },
    "jaeger": {
        "repo": "jaegertracing/jaeger",
        "revision": "806f4447841ecdb60519f408b004a599d515f437",
        "module": "github.com/jaegertracing/jaeger",
        "go_version": "go1.26.6",
        "image": "golang:1.26.6-bookworm@sha256:433f9dc4f8ea3a1ce4e28f9f15d0f7c056b10475307f886d6f1ac1ccc4abd976",
        "go_flags": "-mod=readonly",
        "package_timeout": "5m",
        "short": False,
        "coverage": True,
        "storage": "memory",
        "hotrod": True,
    },
}
PROFILE_SECONDS = 30 * 60
MAX_FILE_BYTES = 256 * 1024 * 1024
CONTEXT = Path(__file__).resolve().parent


def read_artifact(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_FILE_BYTES:
        raise ValueError(f"invalid artifact type or size: {path.name}")
    return path.read_text(encoding="utf-8")


def memory_diagnostics(directory):
    snapshots = {}
    errors = []
    fields = ("memory.current", "memory.peak", "memory.max", "memory.events", "memory.events.local")

    def counter(value):
        if not value.isascii() or not value.isdecimal() or int(value) > 2 ** 64 - 1:
            raise ValueError("expected an unsigned 64-bit integer")
        return int(value)

    for stage in ("start", "before-tests", "after-tests", "exit"):
        values = {}
        for field in fields:
            values[field] = None
            try:
                text = read_artifact(directory / f"memory-{stage}" / field).strip()
                if field.startswith("memory.events"):
                    events = {}
                    for line in text.splitlines():
                        key, value = line.split()
                        if key in events:
                            raise ValueError("duplicate memory event counter")
                        events[key] = counter(value)
                    if not events:
                        raise ValueError("empty memory event counters")
                    values[field] = events
                else:
                    values[field] = "max" if field == "memory.max" and text == "max" else counter(text)
            except (OSError, ValueError) as error:
                errors.append(f"{stage}/{field}: {error}")
        snapshots[stage] = values

    deltas = {}
    for field in ("memory.events", "memory.events.local"):
        before = snapshots["before-tests"][field]
        after = snapshots["after-tests"][field]
        deltas[field] = None
        if before is None or after is None:
            continue
        if before.keys() != after.keys() or any(after[key] < value for key, value in before.items()):
            errors.append(f"{field}: inconsistent counters across the test window")
            continue
        deltas[field] = {key: after[key] - value for key, value in before.items()}
    peaks = [values["memory.peak"] for values in snapshots.values() if values["memory.peak"] is not None]
    return {
        "source": "private_cgroup_v2",
        "peak_scope": "container and descendants since cgroup creation, including setup; not test-only or per-process RSS",
        "event_scope": "memory.events is hierarchical; memory.events.local excludes descendants",
        "snapshots": snapshots,
        "observed_peak_bytes": max(peaks, default=None),
        "test_event_deltas": deltas,
        "errors": errors,
    }


def decode_documents(text):
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(text):
        if text[offset].isspace():
            offset += 1
            continue
        value, offset = decoder.raw_decode(text, offset)
        if not isinstance(value, dict):
            raise ValueError("expected a JSON object")
        yield value


def test_targets(text, module):
    packages = list(decode_documents(text))
    if not packages:
        raise ValueError("package discovery returned no packages")
    seen = set()
    targets = set()
    for package in packages:
        name = package.get("ImportPath", "")
        if not isinstance(name, str) or not name or name in seen or package.get("Error"):
            raise ValueError("invalid or duplicate discovered package")
        module_data = package.get("Module") or {}
        if not isinstance(module_data, dict) or module_data.get("Path") != module or not (name == module or name.startswith(module + "/")):
            raise ValueError("discovery escaped the declared root module")
        seen.add(name)
        if package.get("TestGoFiles") or package.get("XTestGoFiles"):
            targets.add(name)
    if not targets:
        raise ValueError("package discovery returned no test targets")
    return targets


def test_summary(text, targets):
    terminal = {}
    active = set()
    failures = 0
    build_failures = 0
    pass_events = 0
    for number, line in enumerate(text.splitlines(), 1):
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise ValueError(f"malformed test event at line {number}") from error
        if not isinstance(event, dict):
            raise ValueError(f"non-object test event at line {number}")
        action = event.get("Action")
        package = event.get("Package")
        test = event.get("Test")
        if action == "build-fail" or event.get("FailedBuild"):
            build_failures += 1
        if not package:
            continue
        if not test and action in ("pass", "fail", "skip"):
            if package in terminal:
                raise ValueError(f"duplicate terminal event for {package}")
            terminal[package] = action
        if test and action == "fail":
            failures += 1
        if test and action == "pass" and package in targets:
            active.add(package)
            pass_events += 1
    return {
        "declared_test_targets": len(targets),
        "active_test_targets": len(active),
        "missing_targets": sorted(targets - terminal.keys()),
        "skipped_targets": sorted(p for p in targets if terminal.get(p) == "skip"),
        "targets_without_pass_events": sorted(targets - active),
        "failed_packages": sorted(p for p, action in terminal.items() if action == "fail"),
        "test_failure_events": failures,
        "test_pass_events": pass_events,
        "build_failure_events": build_failures,
    }


def evaluate_artifacts(directory, profile, exit_code):
    if not (directory / "test-exit.txt").exists():
        return {"status": "setup_failed", "error": "worker did not reach a completed test invocation"}
    test_exit = int(read_artifact(directory / "test-exit.txt").strip())
    worker_exit = int(read_artifact(directory / "worker-exit.txt").strip())
    actual_revision = read_artifact(directory / "checkout.txt").strip()
    go_env = json.loads(read_artifact(directory / "go-env.json"))
    if actual_revision != profile["revision"]:
        raise ValueError("checkout does not match the pinned revision")
    for field, expected in {
        "GOVERSION": profile["go_version"],
        "GOOS": "linux",
        "GOARCH": "amd64",
        "CGO_ENABLED": "1",
        "GOFLAGS": profile["go_flags"],
        "GOTOOLCHAIN": "local",
    }.items():
        if go_env.get(field) != expected:
            raise ValueError(f"unexpected build context: {field}")
    if read_artifact(directory / "storage.txt").strip() != profile["storage"]:
        raise ValueError("unexpected storage profile")
    targets = test_targets(read_artifact(directory / "packages.json"), profile["module"])
    details = test_summary(read_artifact(directory / "go-test.jsonl"), targets)
    details["test_exit"] = test_exit
    details["test_wall_seconds"] = float(read_artifact(directory / "test-wall-seconds.txt").strip())
    if not math.isfinite(details["test_wall_seconds"]) or details["test_wall_seconds"] < 0:
        raise ValueError("test wall time must be finite and nonnegative")
    if read_artifact(directory / "tracked-changes.txt").strip():
        details["status"] = "source_modified"
    elif exit_code or worker_exit or test_exit or details["failed_packages"] or details["test_failure_events"] or details["build_failure_events"]:
        details["status"] = "test_failed"
    elif details["missing_targets"] or details["skipped_targets"] or not details["test_pass_events"]:
        details["status"] = "incomplete"
    else:
        details["status"] = "qualified"
    return details


def run_command(arguments, log, deadline, capture=False, check=True):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise subprocess.TimeoutExpired(arguments, 0)
    with log.open("a", encoding="utf-8") as output:
        output.write(json.dumps(arguments) + "\n")
        output.flush()
        return subprocess.run(
            arguments,
            stdout=subprocess.PIPE if capture else output,
            stderr=output,
            text=True,
            timeout=remaining,
            check=check,
        )


def container_command(name, image, directory, profile_name, profile):
    return [
        "docker", "run", "--rm", "--name", name,
        "--platform", "linux/amd64", "--cpus", "2", "--memory", "6g",
        "--memory-swap", "6g", "--pids-limit", "2048",
        "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true",
        "--mount", f"type=bind,src={directory},dst=/output",
        image, profile_name, profile["repo"], profile["revision"],
        profile["go_version"], profile["go_flags"], profile["package_timeout"],
        str(int(profile["short"])), str(int(profile["coverage"])),
        profile["storage"], str(int(profile["hotrod"])),
    ]


def remove_artifact_links(directory):
    removed = []
    for root, directories, files in os.walk(directory, followlinks=False):
        for name in directories + files:
            path = Path(root) / name
            if path.is_symlink():
                removed.append(str(path.relative_to(directory)))
                path.unlink()
    return sorted(removed)


def qualify_profile(profile_name, directory, uid, gid):
    profile = PROFILES[profile_name]
    record = {"profile": profile_name, "configuration": profile, "status": "setup_failed"}
    directory.mkdir()
    log = directory / "docker.log"
    started = time.monotonic()
    deadline = started + PROFILE_SECONDS
    name = f"jevci-qualify-{profile_name}-{uuid.uuid4().hex[:12]}"
    image = name + ":local"
    try:
        run_command(["docker", "pull", "--platform", "linux/amd64", profile["image"]], log, deadline)
        inspected = run_command(["docker", "image", "inspect", profile["image"], "--format", "{{json .Id}}"], log, deadline, capture=True)
        record["base_image_id"] = json.loads(inspected.stdout)
        run_command([
            "docker", "build", "--platform", "linux/amd64",
            "--build-arg", f"GO_IMAGE={profile['image']}",
            "--build-arg", f"LOCAL_UID={uid}", "--build-arg", f"LOCAL_GID={gid}",
            "--tag", image, str(CONTEXT),
        ], log, deadline)
        result = run_command(container_command(name, image, directory, profile_name, profile), log, deadline, check=False)
        record["container_exit"] = result.returncode
        links = remove_artifact_links(directory)
        if links:
            raise ValueError(f"removed unsafe artifact links: {links}")
        record.update(evaluate_artifacts(directory, profile, result.returncode))
    except subprocess.TimeoutExpired:
        record.update(status="timeout", error="30-minute profile limit reached; no automatic retry")
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        record.update(status="setup_failed", error=str(error))
    except KeyboardInterrupt:
        record.update(status="cancelled", cancelled=True, error="qualification cancelled")
    finally:
        try:
            run_command(["docker", "stop", "--time", "10", name], log, time.monotonic() + 20, check=False)
            running = run_command(["docker", "ps", "--quiet", "--filter", f"name=^/{name}$"], log, time.monotonic() + 10, capture=True)
            if running.stdout.strip():
                raise OSError("worker remains running after cleanup")
        except (OSError, subprocess.SubprocessError) as error:
            record["cleanup_error"] = str(error)
            record["status"] = "cleanup_failed"
        links = remove_artifact_links(directory)
        if links:
            record.update(status="unsafe_artifacts", removed_links=links)
        record["memory_diagnostics"] = memory_diagnostics(directory)
        record["elapsed_seconds"] = round(time.monotonic() - started, 3)
        (directory / "result.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    return record


def write_run(output, metadata, results):
    document = dict(metadata, results=results)
    (output / "run.json").write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
    lines = [
        "# Environment qualification", "",
        "This is not a held-out evaluation or a test-selection result. No Jev credentials are supplied.", "",
        "| Profile | Status | Total seconds including setup | Observed peak GiB including setup | Test-window OOM kills |",
        "|---|---|---|---|---|",
    ]
    for result in results:
        memory = result.get("memory_diagnostics", {})
        peak = memory.get("observed_peak_bytes")
        events = memory.get("test_event_deltas", {}).get("memory.events") or {}
        kills = events.get("oom_kill")
        peak_text = "n/a" if peak is None else f"{peak / (1024 ** 3):.3f}"
        kills_text = "n/a" if kills is None else str(kills)
        lines.append(f"| {result['profile']} | {result['status']} | {result['elapsed_seconds']} | {peak_text} | {kills_text} |")
    lines += [
        "", "Test timings are setup diagnostics, not warm-cache study measurements.",
        "Memory peaks cover the container and descendants, including setup and compilation. OOM kills are counter differences around the test command.",
        "Missing memory diagnostics show n/a, not zero. A forced container kill can prevent final snapshots.", "",
    ]
    (output / "summary.md").write_text("\n".join(lines), encoding="utf-8")


def host_resources():
    try:
        memory = os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES")
    except (OSError, ValueError):
        memory = None
    try:
        lines = Path("/proc/cpuinfo").read_text(encoding="utf-8").splitlines()
        cpu_model = next((line.split(":", 1)[1].strip() for line in lines if line.startswith("model name") and ":" in line), None)
    except OSError:
        cpu_model = None
    return {"host_memory_bytes": memory, "host_cpu_model": cpu_model}


def main(arguments=None):
    parser = argparse.ArgumentParser(description="Qualify fixed Linux test profiles without Jev or bug injection")
    parser.add_argument("--profile", choices=["all", *PROFILES], required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(arguments)
    if os.environ.get("GITHUB_ACTIONS") != "true" or platform.system() != "Linux" or platform.machine() != "x86_64":
        parser.error("run this command through the manual Linux GitHub Actions workflow")
    if os.getuid() == 0 or os.getgid() == 0:
        parser.error("qualification requires a non-root runner user and group")
    output = args.output.resolve()
    output.mkdir(parents=False, exist_ok=False)
    metadata = {
        "version": 1,
        "purpose": "environment_qualification",
        "eligible_as_heldout_result": False,
        "jev_enabled": False,
        "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "workflow_commit": os.environ.get("GITHUB_SHA"),
        "workflow_run_id": os.environ.get("GITHUB_RUN_ID"),
        "host_platform": platform.platform(),
        "host_cpu_count": os.cpu_count(),
        "runner_image": os.environ.get("ImageOS"),
        "runner_image_version": os.environ.get("ImageVersion"),
        "container_cpu_limit": 2,
        "container_memory_limit_gib": 6,
        "per_profile_timeout_seconds": PROFILE_SECONDS,
        "network": "public network access; no host credentials, host process namespace, or Docker socket mounted",
        "billing": "workflow timeout does not enforce a dollar cap; owner confirms allowance and spending controls before dispatch",
    }
    metadata.update(host_resources())
    results = []
    write_run(output, metadata, results)
    names = list(PROFILES) if args.profile == "all" else [args.profile]
    try:
        for name in names:
            print(f"Qualifying {name}; maximum {PROFILE_SECONDS // 60} minutes", flush=True)
            result = qualify_profile(name, output / name, os.getuid(), os.getgid())
            results.append(result)
            write_run(output, metadata, results)
            print(f"{name}: {result['status']}", flush=True)
            if result.get("cancelled"):
                metadata["cancelled"] = True
                write_run(output, metadata, results)
                return 130
            if result["status"] == "cleanup_failed":
                for pending in names[len(results):]:
                    results.append({"profile": pending, "status": "not_run", "elapsed_seconds": 0, "error": "previous worker cleanup failed"})
                write_run(output, metadata, results)
                break
    except KeyboardInterrupt:
        metadata["cancelled"] = True
        write_run(output, metadata, results)
        return 130
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a", encoding="utf-8") as summary:
            summary.write(read_artifact(output / "summary.md"))
    return 0 if all(result["status"] == "qualified" for result in results) else 1


def interrupt(_signum, _frame):
    raise KeyboardInterrupt


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupt)
    sys.exit(main())
