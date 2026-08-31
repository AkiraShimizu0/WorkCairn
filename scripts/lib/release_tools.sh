# scripts/lib/release_tools.sh
#
# Secure tool-boundary structure for macOS release packaging
# (ADR-0071). This is Slice 1 (PB-3o.2) scope only: the fixed
# tool-bundle variable contract and its validation function. It does
# NOT implement any signing, DMG, notarization, cleanup, or promotion
# workflow -- that is explicit Slice 2 (PB-3o.3) scope and must not be
# added here.
#
# Contract:
#   - Every tool path this library validates must be supplied by the
#     caller as an explicit variable assignment (see
#     release_tools_required_variables below). This library never
#     performs a PATH search and never falls back to an unqualified
#     command name.
#   - A production caller assigns literal, fixed, absolute paths.
#   - A test caller assigns paths into a temporary fake-tool
#     directory. The two are separated structurally: this library
#     only ever reads whatever the caller already assigned to these
#     variable names -- it has no code path that reads an
#     environment-variable override, and no code path that lets a
#     test bundle reach a production caller.
#   - No retry, no fallback: a single validation pass either accepts
#     or rejects the whole bundle.
#
# This file is sourced by both a future production entrypoint (Slice
# 2) and by scripts/test/release-fake-tool-test.sh (Slice 1). It is
# itself never executed directly.

# release_tools_required_variables
# Prints, one per line, the full set of variable names a caller must
# assign before calling release_tools_validate_bundle. This is the
# fixed tool bundle from ADR-0071 (8 Apple tools) plus the
# release-mutating filesystem commands (4 tools) that Slice 2's
# cleanup/promotion contract will also require fixed paths for.
release_tools_required_variables() {
  printf '%s\n' \
    RELEASE_TOOL_SECURITY \
    RELEASE_TOOL_CODESIGN \
    RELEASE_TOOL_HDIUTIL \
    RELEASE_TOOL_XCRUN \
    RELEASE_TOOL_SPCTL \
    RELEASE_TOOL_PLUTIL \
    RELEASE_TOOL_FILE \
    RELEASE_TOOL_LIPO \
    RELEASE_TOOL_MKDIR \
    RELEASE_TOOL_LN \
    RELEASE_TOOL_RM \
    RELEASE_TOOL_MKTEMP
}

# release_tools_validate_tool_path <path>
# Returns 0 only if path is non-empty, absolute (begins with "/"),
# exists, is a regular file (not a symlink, not a directory), and is
# executable. Never performs a PATH search -- the caller must supply
# an absolute path. A relative path or a bare command name is rejected
# by shape alone, before any filesystem check: a command word without
# a "/" is always subject to PATH search at invocation time regardless
# of what an earlier existence check happened to find relative to the
# current working directory, so accepting one here would silently
# reopen the exact PATH-shadowing gap this fixed-bundle contract
# exists to close.
release_tools_validate_tool_path() {
  _release_tools_path="$1"
  if [ -z "$_release_tools_path" ]; then
    return 1
  fi
  case "$_release_tools_path" in
    /*) : ;;
    *) return 1 ;;
  esac
  if [ -L "$_release_tools_path" ]; then
    return 1
  fi
  if [ ! -f "$_release_tools_path" ]; then
    return 1
  fi
  if [ ! -x "$_release_tools_path" ]; then
    return 1
  fi
  return 0
}

# release_tools_validate_bundle
# Validates every variable named by release_tools_required_variables
# against release_tools_validate_tool_path. Returns 0 only if all are
# valid. Fails closed (returns 1) on the first missing, wrong-type, or
# non-executable entry -- no partial bundle is ever accepted, and no
# fallback to any other path or tool is attempted.
release_tools_validate_bundle() {
  for _release_tools_variable_name in $(release_tools_required_variables); do
    eval "_release_tools_value=\"\${$_release_tools_variable_name:-}\""
    if ! release_tools_validate_tool_path "$_release_tools_value"; then
      return 1
    fi
  done
  return 0
}
