#!/bin/sh
set -eu

# Fake-tool seam foundation (PB-3o.2 Slice 1). Exercises
# scripts/lib/release_tools.sh's tool-bundle validation against an
# explicitly injected fake bundle -- never a production environment
# override, never a real Apple tool. Each scenario below runs exactly
# once (no retry-until-green) against its own fresh, isolated
# temporary copy of the fake-tool directory, so scenarios never share
# mutable state and are safe to add to in parallel.
#
# This driver validates only the generic tool-boundary contract
# (existence/type/executable/symlink). It does not exercise, and must
# not be extended in this Slice to exercise, any notarization,
# hdiutil, signing, cleanup, or promotion behavior -- those schemas
# are not yet characterized (ADR-0071 PB-3o.2n/PB-3o.3 hard gates) and
# no speculative fixture for them is created here.

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
release_tools_lib="$repository_root/scripts/lib/release_tools.sh"
fake_tools_source="$repository_root/scripts/test/fake-tools"
scenario_manifest="$repository_root/scripts/test/fixtures/tool-boundary/scenarios.txt"

. "$release_tools_lib"

pass_count=0
fail_count=0

report_pass() {
  pass_count=$((pass_count + 1))
  echo "PASS: $1"
}

report_fail() {
  fail_count=$((fail_count + 1))
  echo "FAIL: $1" >&2
}

# assign_bundle <dir>
# Points every RELEASE_TOOL_* variable at the fake tool of the same
# name inside <dir>. This is the explicit-injection seam: nothing here
# reads an environment override, and nothing a production caller does
# can make this test bundle reach it.
assign_bundle() {
  _assign_dir="$1"
  RELEASE_TOOL_SECURITY="$_assign_dir/security"
  RELEASE_TOOL_CODESIGN="$_assign_dir/codesign"
  RELEASE_TOOL_HDIUTIL="$_assign_dir/hdiutil"
  RELEASE_TOOL_XCRUN="$_assign_dir/xcrun"
  RELEASE_TOOL_SPCTL="$_assign_dir/spctl"
  RELEASE_TOOL_PLUTIL="$_assign_dir/plutil"
  RELEASE_TOOL_FILE="$_assign_dir/file"
  RELEASE_TOOL_LIPO="$_assign_dir/lipo"
  RELEASE_TOOL_MKDIR="$_assign_dir/mkdir"
  RELEASE_TOOL_LN="$_assign_dir/ln"
  RELEASE_TOOL_RM="$_assign_dir/rm"
  RELEASE_TOOL_MKTEMP="$_assign_dir/mktemp"
}

# fresh_scenario_dir
# Creates and prints a new, isolated temporary directory containing a
# full copy of the fake-tool bundle. Each scenario gets its own copy
# -- no shared mutable state, safe to run scenarios in any order or in
# parallel.
fresh_scenario_dir() {
  _scenario_dir=$(mktemp -d "${TMPDIR:-/tmp}/workcairn-release-fake-tool-test.XXXXXX")
  cp -R "$fake_tools_source"/. "$_scenario_dir"/
  printf '%s\n' "$_scenario_dir"
}

run_scenario_success() {
  _dir=$(fresh_scenario_dir)
  assign_bundle "$_dir"
  if release_tools_validate_bundle; then
    report_pass "success: full fake bundle validates"
  else
    report_fail "success: full fake bundle should validate but did not"
  fi
  rm -rf "$_dir"
}

run_scenario_missing() {
  _dir=$(fresh_scenario_dir)
  assign_bundle "$_dir"
  rm -f "$_dir/codesign"
  if release_tools_validate_bundle; then
    report_fail "missing: bundle with a missing tool should not validate"
  else
    report_pass "missing: bundle with a missing tool correctly fails"
  fi
  rm -rf "$_dir"
}

run_scenario_wrong_type_directory() {
  _dir=$(fresh_scenario_dir)
  assign_bundle "$_dir"
  rm -f "$_dir/hdiutil"
  mkdir "$_dir/hdiutil"
  if release_tools_validate_bundle; then
    report_fail "wrong_type_directory: a directory in place of a tool should not validate"
  else
    report_pass "wrong_type_directory: a directory in place of a tool correctly fails"
  fi
  rm -rf "$_dir"
}

run_scenario_non_executable() {
  _dir=$(fresh_scenario_dir)
  assign_bundle "$_dir"
  chmod -x "$_dir/spctl"
  if release_tools_validate_bundle; then
    report_fail "non_executable: a non-executable tool should not validate"
  else
    report_pass "non_executable: a non-executable tool correctly fails"
  fi
  rm -rf "$_dir"
}

run_scenario_symlink() {
  _dir=$(fresh_scenario_dir)
  assign_bundle "$_dir"
  rm -f "$_dir/xcrun"
  ln -s "$_dir/security" "$_dir/xcrun"
  if release_tools_validate_bundle; then
    report_fail "symlink: a symlinked tool path should not validate"
  else
    report_pass "symlink: a symlinked tool path correctly fails"
  fi
  rm -rf "$_dir"
}

run_scenario_invocation_log_isolation() {
  _dir_a=$(fresh_scenario_dir)
  _dir_b=$(fresh_scenario_dir)
  _log_a="$_dir_a/invocations.log"
  _log_b="$_dir_b/invocations.log"

  FAKE_TOOL_LOG="$_log_a" "$_dir_a/security" find-identity-scenario-a >/dev/null
  FAKE_TOOL_LOG="$_log_b" "$_dir_b/security" find-identity-scenario-b >/dev/null

  if grep -q "scenario-a" "$_log_a" 2>/dev/null \
     && ! grep -q "scenario-b" "$_log_a" 2>/dev/null \
     && grep -q "scenario-b" "$_log_b" 2>/dev/null \
     && ! grep -q "scenario-a" "$_log_b" 2>/dev/null; then
    report_pass "invocation_log_isolation: two scenarios' logs never cross-contaminate"
  else
    report_fail "invocation_log_isolation: scenario logs leaked into each other"
  fi
  rm -rf "$_dir_a" "$_dir_b"
}

run_scenario_relative_path_rejected() {
  _dir=$(fresh_scenario_dir)
  if release_tools_validate_tool_path "$(basename "$_dir")/security"; then
    report_fail "relative_path_rejected: a relative tool path should not validate"
  else
    report_pass "relative_path_rejected: a relative tool path correctly fails"
  fi
  rm -rf "$_dir"
}

run_scenario_bare_command_rejected() {
  if release_tools_validate_tool_path "security"; then
    report_fail "bare_command_rejected: a bare command name should not validate"
  else
    report_pass "bare_command_rejected: a bare command name correctly fails"
  fi
}

run_scenario_valid_absolute_path_accepted() {
  _dir=$(fresh_scenario_dir)
  if release_tools_validate_tool_path "$_dir/security"; then
    report_pass "valid_absolute_path_accepted: a valid absolute tool path validates"
  else
    report_fail "valid_absolute_path_accepted: a valid absolute tool path should validate but did not"
  fi
  rm -rf "$_dir"
}

while IFS= read -r scenario_name || [ -n "$scenario_name" ]; do
  [ -n "$scenario_name" ] || continue
  case "$scenario_name" in
    success) run_scenario_success ;;
    missing) run_scenario_missing ;;
    wrong_type_directory) run_scenario_wrong_type_directory ;;
    non_executable) run_scenario_non_executable ;;
    symlink) run_scenario_symlink ;;
    *)
      report_fail "unknown scenario in manifest: $scenario_name"
      ;;
  esac
done < "$scenario_manifest"

run_scenario_invocation_log_isolation
run_scenario_relative_path_rejected
run_scenario_bare_command_rejected
run_scenario_valid_absolute_path_accepted

echo "release-fake-tool-test: $pass_count passed, $fail_count failed"
if [ "$fail_count" -ne 0 ]; then
  exit 1
fi
exit 0
