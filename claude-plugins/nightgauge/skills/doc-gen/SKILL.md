---
name: doc-gen
description: Auto-generate and update documentation for public APIs. Detects undocumented
  functions, generates JSDoc/docstrings, identifies signature changes, and
  suggests README updates. Use after /feature-dev or standalone on any codebase.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
disable-model-invocation: true
---

# Auto-Documentation Generation

> Detect undocumented APIs, generate documentation stubs, and keep docs in sync
> with code

## Description

This skill automatically generates and updates documentation by:

1. Detecting public APIs that lack documentation
2. Generating appropriate documentation stubs (JSDoc, docstrings, GoDoc, etc.)
3. Identifying signature changes in already-documented APIs
4. Suggesting README updates for new features

## Invocation

| Tool           | Command                            |
| -------------- | ---------------------------------- |
| Claude Code    | `/nightgauge:doc-gen` (via plugin) |
| OpenAI Codex   | `$nightgauge-doc-gen`              |
| GitHub Copilot | Invoke via Agent Skills            |
| Cursor         | Invoke via Agent Skills            |

## Arguments

```bash
# After feature-dev (uses dev context for changed files)
/nightgauge:doc-gen

# Scan specific files
/nightgauge:doc-gen --files "src/services/*.ts"

# Report only, no changes
/nightgauge:doc-gen --report-only

# Skip README suggestions
/nightgauge:doc-gen --skip-readme

# Generate for all public APIs (not just undocumented)
/nightgauge:doc-gen --all
```

## Prerequisites

- **Source code to document**: Either specify with `--files` or have changes in
  `dev-{N}.json` context
- **Language detection**: Auto-detects based on file extensions

## Philosophy

- **Non-invasive** — Never overwrites existing documentation without asking
- **Context-aware** — Uses function names, parameters, and return types to
  generate meaningful descriptions
- **Standards-compliant** — Follows project documentation style from
  `docs/CODE_STANDARDS.md`
- **Flexible** — Works standalone or as part of the pipeline

---

## Workflow

### Phase 0: Context Loading

<!-- include: ../_shared/PREFLIGHT.md -->

---

#### Step 0.1: Extract Issue from Branch

**CRITICAL**: This skill is stateless. Extract issue context from branch name:

```bash
# Get current branch
BRANCH=$(git branch --show-current)

# Extract issue number from branch (e.g., feat/42-description -> 42)
ISSUE_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)

echo "Branch: $BRANCH"
echo "Issue: #$ISSUE_NUMBER"
```

#### Step 0.2: Check for Dev Context

Look for dev context file from `/feature-dev`:

```bash
# Check for dev context
CONTEXT_FILE=".nightgauge/pipeline/dev-${ISSUE_NUMBER}.json"

if [ -f "$CONTEXT_FILE" ]; then
  echo "Found dev context: $CONTEXT_FILE"
  # Extract files_changed from JSON
else
  echo "No dev context found, will scan project"
fi
```

#### Step 0.3: Parse Arguments

Check for provided arguments:

- `--files` - Specific file glob pattern
- `--report-only` - Only report, don't make changes
- `--skip-readme` - Skip README update suggestions
- `--all` - Document all public APIs, not just undocumented ones

---

### Phase 1: Project Analysis

#### Step 1.1: Detect Languages and Frameworks

Scan project for language indicators:

```bash
# Check for TypeScript/JavaScript
ls *.ts *.tsx *.js *.jsx package.json tsconfig.json 2>/dev/null

# Check for Python
ls *.py pyproject.toml setup.py requirements.txt 2>/dev/null

# Check for Go
ls *.go go.mod go.sum 2>/dev/null

# Check for Rust
ls *.rs Cargo.toml 2>/dev/null

# Check for Java
ls *.java pom.xml build.gradle 2>/dev/null

# Check for C#
ls *.cs *.csproj 2>/dev/null
```

#### Step 1.2: Load Documentation Style

If `docs/CODE_STANDARDS.md` exists, extract documentation conventions:

- Comment style preferences
- Required documentation elements (@param, @returns, @throws)
- Example formatting

#### Step 1.3: Report Detection Results

```
Project Analysis
================
Languages detected: TypeScript, JavaScript
Framework: Node.js/Express
Documentation style: JSDoc (/** */)
Code standards: docs/CODE_STANDARDS.md found

Target files:
- From dev context: 5 files changed
- Or: Scanning src/**/*.ts
```

---

### Phase 2: API Detection

#### Step 2.1: Identify Target Files

If `--files` argument provided:

```bash
FILES=$(ls $FILES_ARG 2>/dev/null)
```

If dev context exists:

```bash
# Extract files_changed from dev-{N}.json
FILES=$(jq -r '.files_changed[]' "$CONTEXT_FILE" 2>/dev/null)
```

Otherwise, scan project using parallel Glob calls (faster than `find`):

```
# Find all source files — run these Glob calls in parallel
Glob("**/*.ts")   Glob("**/*.js")   Glob("**/*.py")   Glob("**/*.go")

Exclude: node_modules/, .git/, vendor/, dist/, build/
```

From the combined results, filter out files matching test patterns
(`*.test.*`, `*.spec.*`). Remaining files are `FILES`.

#### Step 2.2: Detect Public APIs per Language

**TypeScript/JavaScript:**

```typescript
// Detect: export function, export class, export const, public methods
export function publicApi() {} // ✓ Needs docs
export class Service {} // ✓ Needs docs
export const handler = () => {}; // ✓ Needs docs
function privateHelper() {} // ✗ Skip (not exported)
```

Pattern to match:

```regex
^export\s+(function|class|const|let|var|interface|type|enum)\s+\w+
```

**Python:**

```python
# Detect: module-level functions (no leading _), classes, __all__ members
def public_function():  # ✓ Needs docs
    pass

def _private_helper():  # ✗ Skip (leading underscore)
    pass

class PublicClass:      # ✓ Needs docs
    pass
```

Pattern to match:

```regex
^(def|class)\s+[^_]\w+
```

**Go:**

```go
// Detect: Capitalized names (exported)
func PublicFunction() {}  // ✓ Needs docs
func privateFunction() {} // ✗ Skip (lowercase)

type PublicStruct struct {} // ✓ Needs docs
```

Pattern to match:

```regex
^func\s+[A-Z]\w+|^type\s+[A-Z]\w+
```

**Rust:**

```rust
// Detect: pub functions, structs, enums, traits
pub fn public_function() {} // ✓ Needs docs
fn private_function() {}    // ✗ Skip (no pub)
```

**Java:**

```java
// Detect: public methods, classes
public void publicMethod() {} // ✓ Needs docs
private void helper() {}      // ✗ Skip (private)
```

**C#:**

```csharp
// Detect: public methods, classes
public void PublicMethod() {} // ✓ Needs docs
private void Helper() {}      // ✗ Skip (private)
```

#### Step 2.3: Check for Existing Documentation

For each detected public API, check if documentation exists:

**JSDoc (TypeScript/JavaScript):**

```regex
/\*\*[\s\S]*?\*/\s*export
```

**Docstring (Python):**

```regex
def \w+\([^)]*\):\s*"""
```

**GoDoc (Go):**

```regex
//\s+\w+\s+.*\nfunc\s+[A-Z]
```

#### Step 2.4: Build API Report

```
API Detection Report
====================

File: src/services/UserService.ts
---------------------------------
✓ Documented:  createUser (line 15)
✗ Undocumented: updateUser (line 45)
✗ Undocumented: deleteUser (line 72)
⚠ Outdated:    getUser (signature changed, line 30)

File: src/utils/validation.ts
-----------------------------
✗ Undocumented: validateEmail (line 8)
✗ Undocumented: validatePhone (line 22)
✓ Documented:  validatePassword (line 35)

Summary:
- Total public APIs: 12
- Documented: 4 (33%)
- Undocumented: 6
- Potentially outdated: 2
```

---

### Phase 3: Signature Change Detection

#### Step 3.1: Parse Existing Documentation

For documented APIs, extract:

- Parameter names and types from documentation
- Return type from documentation
- Documented exceptions/errors

#### Step 3.2: Parse Current Signatures

Extract from code:

- Actual parameter names and types
- Actual return type
- Thrown exceptions

#### Step 3.3: Compare and Flag Mismatches

```
Signature Changes Detected
==========================

UserService.getUser:
  Documentation says:
    @param id: string - User ID
    @returns User

  Code signature:
    @param id: string
    @param options?: GetUserOptions  ← NEW PARAMETER
    @returns Promise<User | null>    ← CHANGED RETURN TYPE

Recommendation: Update documentation to match new signature
```

#### Step 3.4: Handle Partial Documentation

If a function has some documentation but is incomplete (e.g., missing @param
tags):

```
Incomplete Documentation
========================

validateEmail (src/utils/validation.ts:8):
  Has: Description
  Missing: @param email, @returns, @throws

Recommendation: Add missing parameter and return documentation
```

---

### Phase 4: Documentation Generation

#### Step 4.1: Confirm with User (unless --report-only)

```json
{
  "questions": [
    {
      "question": "Found 6 undocumented APIs. Generate documentation stubs?",
      "header": "Generate",
      "multiSelect": false,
      "options": [
        {
          "label": "Generate all",
          "description": "Create documentation for all 6 undocumented APIs"
        },
        {
          "label": "Review each",
          "description": "Show proposed documentation one by one for approval"
        },
        {
          "label": "Skip",
          "description": "Don't generate any documentation"
        }
      ]
    }
  ]
}
```

#### Step 4.2: Generate Documentation Stubs

Based on detected language, generate appropriate format:

**JSDoc (TypeScript/JavaScript):**

```typescript
/**
 * Updates user information in the database.
 *
 * @param id - The unique identifier of the user to update
 * @param updates - Object containing the fields to update
 * @returns The updated user object
 * @throws {NotFoundError} If user with given ID doesn't exist
 */
export async function updateUser(
  id: string,
  updates: UserUpdateInput
): Promise<User> {
```

**Docstring (Python):**

```python
def validate_email(email: str) -> bool:
    """Validate an email address format.

    Args:
        email: The email address to validate.

    Returns:
        True if the email format is valid, False otherwise.

    Raises:
        ValueError: If email is None or empty.
    """
```

**GoDoc (Go):**

```go
// CreateUser creates a new user with the given information.
// It returns the created user and an error if the operation fails.
func CreateUser(name string, email string) (*User, error) {
```

#### Step 4.3: Apply Documentation

Use Edit tool to insert documentation above each function:

```
Applying Documentation
======================
✓ src/services/UserService.ts:45 - updateUser
✓ src/services/UserService.ts:72 - deleteUser
✓ src/utils/validation.ts:8 - validateEmail
✓ src/utils/validation.ts:22 - validatePhone
⚠ src/services/UserService.ts:30 - getUser (updated existing)

5 functions documented
```

#### Step 4.4: Verify Syntax

After applying changes, verify files still parse correctly:

```bash
# TypeScript
npx tsc --noEmit

# Python
python -m py_compile file.py

# Go
go build ./...
```

---

### Phase 5: README Suggestions

#### Step 5.1: Detect New Features

Compare current exports against baseline (if `--skip-readme` not set):

- New exported functions/classes
- New API endpoints
- New CLI commands

#### Step 5.2: Analyze for README Relevance

Not all exports need README documentation. Check for:

- Public-facing APIs
- User-visible features
- Configuration changes
- Breaking changes

#### Step 5.3: Generate Suggestions

````
README Update Suggestions
=========================

New exports detected in src/services/PhotoService.ts:
- uploadPhoto
- resizeImage
- generateThumbnail

These appear to be a new Photo Upload feature. Suggested README section:

## Photo Upload

Upload and manage user photos with automatic resizing.

### Functions

- `uploadPhoto(file, options)` - Upload a photo for the current user
- `generateThumbnail(photoId, size)` - Generate a thumbnail for a photo

### Example

```typescript
const photo = await uploadPhoto(file, { maxSize: 5_000_000 });
const thumb = await generateThumbnail(photo.id, { width: 100, height: 100 });
````

---

Would you like to add this section to README.md?

````

#### Step 5.4: Confirm and Apply

```json
{
  "questions": [
    {
      "question": "Add suggested section to README.md?",
      "header": "README",
      "multiSelect": false,
      "options": [
        { "label": "Add as-is", "description": "Add the suggested section" },
        {
          "label": "Edit first",
          "description": "Let me modify the section before adding"
        },
        { "label": "Skip", "description": "Don't update README" }
      ]
    }
  ]
}
````

---

### Phase 6: Output Context

#### Step 6.1: Generate Context File

Write documentation context for pipeline continuity:

```json
{
  "schema_version": "1.0",
  "issue_number": 28,
  "documentation_generated": {
    "files_modified": ["src/services/UserService.ts", "src/utils/validation.ts"],
    "apis_documented": 5,
    "apis_skipped": 0,
    "apis_already_documented": 4
  },
  "signature_updates": {
    "detected": 2,
    "applied": 2
  },
  "readme_suggestions": {
    "offered": true,
    "accepted": true
  },
  "created_at": "2026-02-02T12:00:00Z"
}
```

Save to `.nightgauge/pipeline/docgen-{N}.json`.

#### Step 6.2: Final Report

```
┌─────────────────────────────────────────────────────────────────┐
│  DOCUMENTATION GENERATION COMPLETE                              │
└─────────────────────────────────────────────────────────────────┘

Branch:  feat/28-auto-documentation-generation
Issue:   #28

## Summary

| Metric              | Count |
|---------------------|-------|
| Files scanned       | 8     |
| Public APIs found   | 12    |
| Already documented  | 4     |
| Newly documented    | 6     |
| Signatures updated  | 2     |
| README updated      | Yes   |

## Files Modified

- src/services/UserService.ts (+3 doc blocks)
- src/utils/validation.ts (+2 doc blocks)
- README.md (+1 section)

## Next Steps

1. Review generated documentation for accuracy
2. Run `/nightgauge:pr-create` to create pull request
3. Or commit manually: git add . && git commit -m "docs: add JSDoc for public APIs"
```

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

### No Source Files Found

```
Warning: No source files found to document.

Possible causes:
1. No files match the --files pattern
2. Project has no recognized source files
3. All source files are in excluded directories

Try:
- /nightgauge:doc-gen --files "src/**/*.ts"
- Check that source files exist and are not in node_modules/
```

### Language Not Supported

```
Warning: Found files with unsupported extensions: .swift, .kt

Currently supported languages:
- TypeScript/JavaScript (.ts, .tsx, .js, .jsx)
- Python (.py)
- Go (.go)
- Rust (.rs)
- Java (.java)
- C# (.cs)

Contribution welcome for additional language support!
```

### Syntax Error After Documentation

```
Warning: Syntax error after applying documentation to src/services/UserService.ts

Error: Unexpected token at line 47

Rolling back changes to this file...

The generated documentation may have formatting issues.
Please review and fix manually, or try again with --review-each flag.
```

---

## Security Considerations

Per `standards/security.md`:

- **No secrets in documentation** — Don't include example credentials
- **No internal details** — Generated docs should describe WHAT, not internal
  HOW
- **Input validation** — Validate `--files` argument to prevent path traversal

---

## Integration with Pipeline

This skill integrates with the Nightgauge pipeline:

```
/issue-pickup → /feature-planning → /feature-dev → /nightgauge:doc-gen → /pr-create
                                                          ↑
                                                     YOU ARE HERE
```

**Input from /feature-dev**: Files changed in implementation (`dev-{N}.json`)
**Output to /pr-create**: Documentation added, ready for review

Can also be used standalone on any codebase.

---

## Source

Part of the
[Nightgauge](https://github.com/nightgauge/nightgauge) -
Issue-to-PR Pipeline.
