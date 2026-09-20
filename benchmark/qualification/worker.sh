#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 10 ]] || exit 2
profile=$1
repo=$2
revision=$3
expected_go=$4
export GOFLAGS=$5
package_timeout=$6
short=$7
coverage=$8
storage=$9
hotrod=${10}
out=/output
src=/work/source
cgroup_root=/sys/fs/cgroup
cgroup_membership=/proc/self/cgroup

capture_memory() {
  local directory="$out/memory-$1" file
  mkdir -p "$directory" || return 0
  if ! cat "$cgroup_membership" > "$directory/cgroup.txt" 2> "$directory/errors.log"; then
    return 0
  fi
  if [[ ! -r "$cgroup_root/cgroup.controllers" || $(< "$directory/cgroup.txt") != "0::/" ]]; then
    printf '%s\n' 'Private cgroup v2 memory counters unavailable' > "$directory/unavailable.txt" || true
    return 0
  fi
  for file in memory.current memory.peak memory.max memory.events memory.events.local; do
    cat "$cgroup_root/$file" > "$directory/$file" 2>> "$directory/errors.log" || true
  done
}

phase() {
  printf '%s\n' "$1" > "$out/phase.txt"
  printf '%s: %s\n' "$profile" "$1"
}

finish() {
  code=$?
  trap - EXIT
  capture_memory exit || true
  if [[ -d "$src/.git" ]] && git -C "$src" rev-parse --verify HEAD >/dev/null 2>&1; then
    git -C "$src" diff --name-only HEAD > "$out/tracked-changes.txt" || code=1
    git -C "$src" status --porcelain=v1 --untracked-files=all > "$out/source-status.txt" || code=1
  fi
  printf '%s\n' "$code" > "$out/worker-exit.txt"
  exit "$code"
}
trap finish EXIT
capture_memory start || true

phase checkout
git init -q "$src"
git -C "$src" fetch -q --depth=1 "https://github.com/$repo.git" "$revision"
git -C "$src" checkout -q --detach FETCH_HEAD
cd "$src"
git rev-parse HEAD > "$out/checkout.txt"
[[ $(git rev-parse HEAD) == "$revision" ]]
go version > "$out/go-version.txt"
[[ $(go env GOVERSION) == "$expected_go" ]]
[[ $(go env GOOS) == linux && $(go env GOARCH) == amd64 ]]
if [[ -n "$storage" ]]; then
  export STORAGE=$storage
fi
go env -json GOVERSION GOOS GOARCH CGO_ENABLED GOFLAGS GOMOD GOWORK GOPATH GOCACHE GOTOOLCHAIN > "$out/go-env.json"
printf '%s\n' "$storage" > "$out/storage.txt"

phase dependencies
go mod download > "$out/dependencies.log" 2>&1
if [[ "$hotrod" == 1 ]]; then
  phase build-hotrod
  CGO_ENABLED=0 go build -trimpath -o ./examples/hotrod/hotrod-linux-amd64 ./examples/hotrod/main.go > "$out/hotrod-build.log" 2>&1
fi

phase discovery
go list -race -json=ImportPath,TestGoFiles,XTestGoFiles,Module,Error ./... > "$out/packages.json" 2> "$out/discovery.stderr"
printf '%s\n' 'go list -race -json=ImportPath,TestGoFiles,XTestGoFiles,Module,Error ./...' > "$out/discovery-command.txt"

args=(-json -count=1 -race -p=2 -timeout="$package_timeout")
if [[ "$short" == 1 ]]; then
  args+=(-short)
fi
if [[ "$coverage" == 1 ]]; then
  args+=(-coverprofile="$out/coverage.out")
fi
printf '%q ' go test "${args[@]}" ./... > "$out/test-command.txt"
printf '\n' >> "$out/test-command.txt"
phase tests
capture_memory before-tests || true
TIMEFORMAT='%3R'
set +e
{ time go test "${args[@]}" ./... > "$out/go-test.jsonl" 2> "$out/go-test.stderr"; } 2> "$out/test-wall-seconds.txt"
status=$?
set -e
capture_memory after-tests || true
printf '%s\n' "$status" > "$out/test-exit.txt"
phase complete
exit "$status"
