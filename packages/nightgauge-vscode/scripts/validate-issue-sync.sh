#!/bin/bash
# Nightgauge Scripts - Validate Issue/Project Synchronization
# Detects and optionally fixes drift between issue labels and project board fields
#
# Usage: validate-issue-sync.sh [OPTIONS]
#
# Options:
#   --fix                  Auto-fix drift by updating project fields to match labels
#   --dry-run              Report what would be fixed without making changes
#   --issue N              Validate specific issue number
#   --all                  Validate all issues in the project
#   --auto-create-labels   Create missing labels automatically (use with --fix)
#   --json                 Output results in JSON format
#
# Examples:
#   validate-issue-sync.sh                    # Validate current branch issue
#   validate-issue-sync.sh --issue 144        # Validate specific issue
#   validate-issue-sync.sh --all              # Validate all project issues
#   validate-issue-sync.sh --fix              # Auto-fix drift for current issue
#   validate-issue-sync.sh --all --fix        # Fix all drifted issues
#
# Environment:
#   NIGHTGAUGE_PROJECT_NUMBER    - Override project number from .nightgauge/config.yaml
#   NIGHTGAUGE_SKIP_VALIDATION   - Skip validation entirely
#   NIGHTGAUGE_HOOKS_DEBUG       - Enable debug logging
#
# Exit codes:
#   0 - Success (no drift or drift fixed)
#   1 - Error (API failure, invalid arguments)
#   2 - Drift detected (report-only mode)
#
# Compatibility: Bash 3.2+ (macOS compatible)

set -euo pipefail

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
GO_BINARY="${GO_BINARY:-$PROJECT_ROOT/bin/nightgauge}"

# Inline logging utilities (replaces deleted common.sh)
log_info()    { echo "[INFO] $*" >&2; }
log_error()   { echo "[ERROR] $*" >&2; }
log_warning() { echo "[WARN] $*" >&2; }
log_debug()   { [[ "${NIGHTGAUGE_HOOKS_DEBUG:-false}" == "true" ]] && echo "[DEBUG] $*" >&2 || true; }

# Inline utility functions (replaces deleted common.sh)
get_current_branch()     { git branch --show-current 2>/dev/null; }
get_issue_from_branch()  { echo "$1" | grep -oE '[0-9]+' | head -1; }
get_repo_owner()         { gh repo view --json owner -q '.owner.login' 2>/dev/null; }
get_project_number() {
  local cfg="$PROJECT_ROOT/.nightgauge/config.yaml"
  if [[ -f "$cfg" ]]; then
    grep -E '^\s*number:' "$cfg" | head -1 | grep -oE '[0-9]+' | head -1
  else
    echo "${NIGHTGAUGE_PROJECT_NUMBER:-}"
  fi
}

# ============================================================================
# Configuration
# ============================================================================

FIX_MODE=false
DRY_RUN=false
SPECIFIC_ISSUE=""
VALIDATE_ALL=false
AUTO_CREATE_LABELS=false
JSON_OUTPUT=false

# Parse command-line arguments
parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --fix)
        FIX_MODE=true
        shift
        ;;
      --dry-run)
        DRY_RUN=true
        shift
        ;;
      --issue)
        SPECIFIC_ISSUE="$2"
        shift 2
        ;;
      --all)
        VALIDATE_ALL=true
        shift
        ;;
      --auto-create-labels)
        AUTO_CREATE_LABELS=true
        shift
        ;;
      --json)
        JSON_OUTPUT=true
        shift
        ;;
      -h|--help)
        sed -n '2,/^$/p' "$0" | sed 's/^# //'
        exit 0
        ;;
      *)
        echo "ERROR: Unknown option: $1" >&2
        echo "Use --help for usage information" >&2
        exit 1
        ;;
    esac
  done
}

# ============================================================================
# Status Mappings (synchronized with `nightgauge project sync-status`)
# ============================================================================

# Map status name to project Status field value.
# Accepts both "in-progress" and legacy "status:in-progress" forms.
map_status_label() {
  local name="${1#status:}"
  case "$name" in
    ready)       echo "Ready" ;;
    blocked)     echo "Backlog" ;;
    needs-info)  echo "Backlog" ;;
    in-progress) echo "In progress" ;;
    in-review)   echo "In review" ;;
    done)        echo "Done" ;;
    *)                  echo "" ;;
  esac
}

# Map project Status field value to status name
map_status_field() {
  local field="$1"
  case "$field" in
    Ready)          echo "ready" ;;
    Backlog)        echo "blocked" ;;
    "In progress")  echo "in-progress" ;;
    "In review")    echo "in-review" ;;
    Done)           echo "done" ;;
    *)              echo "" ;;
  esac
}

# ============================================================================
# Issue Query Functions
# ============================================================================

# Get status labels for an issue
get_issue_status_labels() {
  local issue_num="$1"

  log_debug "validate-issue-sync: Getting labels for issue #$issue_num"

  if ! gh issue view "$issue_num" --json labels --jq '.labels[].name' 2>/dev/null | grep -E '^status:'; then
    log_debug "validate-issue-sync: No status labels found for issue #$issue_num"
    echo ""
  fi
}

# Get project board status field value for an issue
get_project_status_field() {
  local issue_num="$1"
  local project_num

  project_num=$(get_project_number)
  if [[ $? -ne 0 ]]; then
    log_error "validate-issue-sync: Failed to get project number"
    return 1
  fi

  log_debug "validate-issue-sync: Getting project status for issue #$issue_num in project #$project_num"

  # Query project items for the issue
  local status_value
  status_value=$(gh project item-list "$project_num" \
    --owner "$(get_repo_owner)" \
    --format json \
    --limit 200 \
    2>/dev/null | \
    jq -r ".items[] | select(.content.number == $issue_num) | .status" 2>/dev/null || echo "")

  if [[ -z "$status_value" ]]; then
    log_debug "validate-issue-sync: Issue #$issue_num not found in project or has no status"
    return 1
  fi

  echo "$status_value"
}

# Check if issue is in the project
is_issue_in_project() {
  local issue_num="$1"
  local project_num

  project_num=$(get_project_number)
  if [[ $? -ne 0 ]]; then
    return 1
  fi

  # Check if issue exists in project
  local found
  found=$(gh project item-list "$project_num" \
    --owner "$(get_repo_owner)" \
    --format json \
    --limit 200 \
    2>/dev/null | \
    jq -r ".items[] | select(.content.number == $issue_num) | .id" 2>/dev/null || echo "")

  [[ -n "$found" ]]
}

# ============================================================================
# Drift Detection
# ============================================================================

# Detect drift between issue labels and project status
detect_drift() {
  local issue_num="$1"
  local drift_type=""
  local expected_status=""
  local actual_status=""

  # Get issue status label
  local status_label
  status_label=$(get_issue_status_labels "$issue_num" | head -1)

  if [[ -z "$status_label" ]]; then
    log_debug "validate-issue-sync: No status label on issue #$issue_num (not drift, needs label)"
    echo "no_label"
    return 0
  fi

  # Map label to expected field value
  expected_status=$(map_status_label "$status_label")

  if [[ -z "$expected_status" ]]; then
    log_error "validate-issue-sync: Unknown status label '$status_label' on issue #$issue_num"
    echo "unknown_label"
    return 1
  fi

  # Get actual project status
  if ! is_issue_in_project "$issue_num"; then
    log_debug "validate-issue-sync: Issue #$issue_num not in project"
    echo "not_in_project"
    return 0
  fi

  actual_status=$(get_project_status_field "$issue_num")

  if [[ -z "$actual_status" ]]; then
    log_debug "validate-issue-sync: Issue #$issue_num has no project status field"
    echo "no_field"
    return 0
  fi

  # Compare expected vs actual
  if [[ "$expected_status" != "$actual_status" ]]; then
    log_debug "validate-issue-sync: DRIFT DETECTED on issue #$issue_num: label says '$status_label' -> '$expected_status', field says '$actual_status'"
    echo "drift|$status_label|$expected_status|$actual_status"
    return 2
  fi

  log_debug "validate-issue-sync: Issue #$issue_num in sync: $status_label -> $expected_status"
  echo "in_sync"
  return 0
}

# ============================================================================
# Drift Correction
# ============================================================================

# Fix drift by updating project field to match label (source of truth)
fix_drift() {
  local issue_num="$1"
  local status_label="$2"

  log_info "validate-issue-sync: Fixing drift for issue #$issue_num (label: $status_label)"

  if [[ "$DRY_RUN" == "true" ]]; then
    log_info "validate-issue-sync: [DRY-RUN] Would run: $GO_BINARY project sync-status $issue_num $status_label"
    return 0
  fi

  # Call Go binary to sync project status (sync-project-status.sh was removed in #1976)
  if "$GO_BINARY" project sync-status "$issue_num" "$status_label" 2>&1; then
    log_info "validate-issue-sync: Fixed drift for issue #$issue_num"
    return 0
  else
    log_error "validate-issue-sync: Failed to fix drift for issue #$issue_num"
    return 1
  fi
}

# ============================================================================
# Validation Orchestration
# ============================================================================

# Validate a single issue
validate_issue() {
  local issue_num="$1"
  local result

  result=$(detect_drift "$issue_num")
  local exit_code=$?

  case "$result" in
    in_sync)
      log_debug "validate-issue-sync: Issue #$issue_num: In sync"
      echo "$issue_num|in_sync"
      return 0
      ;;
    no_label)
      log_debug "validate-issue-sync: Issue #$issue_num: No status label (skip)"
      echo "$issue_num|no_label"
      return 0
      ;;
    not_in_project)
      log_debug "validate-issue-sync: Issue #$issue_num: Not in project (skip)"
      echo "$issue_num|not_in_project"
      return 0
      ;;
    no_field)
      log_debug "validate-issue-sync: Issue #$issue_num: No status field (skip)"
      echo "$issue_num|no_field"
      return 0
      ;;
    unknown_label)
      log_error "validate-issue-sync: Issue #$issue_num: Unknown label"
      echo "$issue_num|unknown_label"
      return 1
      ;;
    drift*)
      # Parse drift info: drift|label|expected|actual
      local drift_info="${result#drift|}"
      local label="${drift_info%%|*}"
      drift_info="${drift_info#*|}"
      local expected="${drift_info%%|*}"
      local actual="${drift_info#*|}"

      log_warning "validate-issue-sync: Issue #$issue_num: DRIFT (label: $label, expected: $expected, actual: $actual)"

      if [[ "$FIX_MODE" == "true" ]]; then
        if fix_drift "$issue_num" "$label"; then
          echo "$issue_num|drift_fixed|$label|$expected|$actual"
          return 0
        else
          echo "$issue_num|drift_fix_failed|$label|$expected|$actual"
          return 1
        fi
      else
        echo "$issue_num|drift|$label|$expected|$actual"
        return 2
      fi
      ;;
    *)
      log_error "validate-issue-sync: Issue #$issue_num: Unknown result: $result"
      echo "$issue_num|error"
      return 1
      ;;
  esac
}

# Get current issue from branch
get_current_issue() {
  local branch
  branch=$(get_current_branch)

  if [[ -z "$branch" ]]; then
    return 1
  fi

  local issue_num
  issue_num=$(get_issue_from_branch "$branch")

  if [[ -z "$issue_num" ]]; then
    return 1
  fi

  echo "$issue_num"
}

# Get all issues in project
get_all_project_issues() {
  local project_num

  project_num=$(get_project_number)
  if [[ $? -ne 0 ]]; then
    return 1
  fi

  gh project item-list "$project_num" \
    --owner "$(get_repo_owner)" \
    --format json \
    --limit 200 \
    2>/dev/null | \
    jq -r '.items[].content.number' 2>/dev/null || echo ""
}

# ============================================================================
# Output Formatting
# ============================================================================

# JSON output structure
output_json() {
  local issues_checked="$1"
  local in_sync="$2"
  local drift_detected="$3"
  local drift_fixed="$4"
  local errors="$5"
  local skipped="$6"

  cat <<EOF
{
  "schema_version": "1.0",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "summary": {
    "issues_checked": $issues_checked,
    "in_sync": $in_sync,
    "drift_detected": $drift_detected,
    "drift_fixed": $drift_fixed,
    "errors": $errors,
    "skipped": $skipped
  },
  "mode": {
    "fix_mode": $FIX_MODE,
    "dry_run": $DRY_RUN
  }
}
EOF
}

# Human-readable output
output_human() {
  local issues_checked="$1"
  local in_sync="$2"
  local drift_detected="$3"
  local drift_fixed="$4"
  local errors="$5"
  local skipped="$6"

  echo ""
  echo "┌─────────────────────────────────────────────────────────────────┐"
  echo "│  ISSUE SYNCHRONIZATION VALIDATION                               │"
  echo "└─────────────────────────────────────────────────────────────────┘"
  echo ""
  echo "Issues Checked:    $issues_checked"
  echo "In Sync:           $in_sync"
  echo "Drift Detected:    $drift_detected"

  if [[ "$FIX_MODE" == "true" ]]; then
    echo "Drift Fixed:       $drift_fixed"
  fi

  echo "Errors:            $errors"
  echo "Skipped:           $skipped"
  echo ""

  if [[ $drift_detected -gt 0 && "$FIX_MODE" == "false" ]]; then
    echo "Run with --fix to automatically correct drift"
    echo ""
    return 2
  fi

  if [[ $errors -gt 0 ]]; then
    echo "Some validation errors occurred (see logs above)"
    echo ""
    return 1
  fi

  echo "✓ All checked issues are synchronized"
  echo ""
  return 0
}

# ============================================================================
# Main
# ============================================================================

main() {
  parse_args "$@"

  # Check if validation is disabled
  if [[ "${NIGHTGAUGE_SKIP_VALIDATION:-false}" == "true" ]]; then
    log_info "validate-issue-sync: Validation disabled (NIGHTGAUGE_SKIP_VALIDATION=true)"
    exit 0
  fi

  # Check dependencies
  if ! command -v gh &>/dev/null; then
    log_error "validate-issue-sync: gh CLI not found - install from https://cli.github.com"
    exit 1
  fi

  if ! command -v jq &>/dev/null; then
    log_error "validate-issue-sync: jq not found - install from https://stedolan.github.io/jq"
    exit 1
  fi

  # Determine which issues to validate
  local issues_to_validate=()

  if [[ "$VALIDATE_ALL" == "true" ]]; then
    log_info "validate-issue-sync: Validating all project issues"
    mapfile -t issues_to_validate < <(get_all_project_issues)

    if [[ ${#issues_to_validate[@]} -eq 0 ]]; then
      log_error "validate-issue-sync: No issues found in project"
      exit 1
    fi
  elif [[ -n "$SPECIFIC_ISSUE" ]]; then
    log_info "validate-issue-sync: Validating issue #$SPECIFIC_ISSUE"
    issues_to_validate=("$SPECIFIC_ISSUE")
  else
    # Default: validate current branch issue
    local current_issue
    current_issue=$(get_current_issue)

    if [[ -z "$current_issue" ]]; then
      log_info "validate-issue-sync: No issue found in current branch - nothing to validate"

      if [[ "$JSON_OUTPUT" == "true" ]]; then
        output_json 0 0 0 0 0 0
      fi

      exit 0
    fi

    log_info "validate-issue-sync: Validating current issue #$current_issue"
    issues_to_validate=("$current_issue")
  fi

  # Validate each issue
  local issues_checked=0
  local in_sync=0
  local drift_detected=0
  local drift_fixed=0
  local errors=0
  local skipped=0

  for issue_num in "${issues_to_validate[@]}"; do
    ((issues_checked++)) || true

    local validation_result
    validation_result=$(validate_issue "$issue_num")
    local exit_code=$?

    local result_type="${validation_result#*|}"
    result_type="${result_type%%|*}"

    case "$result_type" in
      in_sync)
        ((in_sync++)) || true
        ;;
      drift)
        ((drift_detected++)) || true
        ;;
      drift_fixed)
        ((drift_fixed++)) || true
        ((drift_detected++)) || true
        ;;
      drift_fix_failed)
        ((errors++)) || true
        ((drift_detected++)) || true
        ;;
      no_label|not_in_project|no_field)
        ((skipped++)) || true
        ;;
      unknown_label|error)
        ((errors++)) || true
        ;;
    esac
  done

  # Output results
  if [[ "$JSON_OUTPUT" == "true" ]]; then
    output_json "$issues_checked" "$in_sync" "$drift_detected" "$drift_fixed" "$errors" "$skipped"

    if [[ $drift_detected -gt 0 && "$FIX_MODE" == "false" ]]; then
      exit 2
    elif [[ $errors -gt 0 ]]; then
      exit 1
    else
      exit 0
    fi
  else
    output_human "$issues_checked" "$in_sync" "$drift_detected" "$drift_fixed" "$errors" "$skipped"
    exit $?
  fi
}

main "$@"
