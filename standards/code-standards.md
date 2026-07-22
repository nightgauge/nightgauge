# Nightgauge Code Standards

Universal coding standards that apply across all AI tools and projects.

## Core Principles

1. **Clarity over cleverness** - Code should be easy to read and understand
2. **Consistency** - Follow established patterns in the codebase
3. **Testability** - Write code that can be easily tested
4. **Security** - Consider security implications in every change

## Naming Conventions

| Element           | Style                    | Example                               |
| ----------------- | ------------------------ | ------------------------------------- |
| Classes/Types     | PascalCase               | `UserService`, `OrderItem`            |
| Functions/Methods | camelCase                | `getUserById`, `calculateTotal`       |
| Variables         | camelCase                | `userName`, `orderCount`              |
| Constants         | SCREAMING_SNAKE_CASE     | `MAX_RETRY_COUNT`, `API_BASE_URL`     |
| Files             | kebab-case or PascalCase | `user-service.ts` or `UserService.ts` |
| Database tables   | snake_case               | `user_accounts`, `order_items`        |

## Code Organization

### File Structure

- One primary export per file
- Group related functionality together
- Keep files focused and manageable (<300 lines ideally)

### Function Design

- Single responsibility - one function, one job
- Clear inputs and outputs
- Minimal side effects
- Reasonable length (<50 lines ideally)

### Error Handling

```typescript
// Good: Explicit error handling with context
try {
  const user = await userService.getById(userId);
  if (!user) {
    throw new NotFoundError(`User not found: ${userId}`);
  }
  return user;
} catch (error) {
  logger.error("Failed to get user", { userId, error });
  throw error;
}

// Bad: Silent failures or generic errors
try {
  return await userService.getById(userId);
} catch (e) {
  return null; // Swallowed error!
}
```

## Documentation

### When to Comment

- **Do comment**: Why something is done (business logic, workarounds)
- **Don't comment**: What the code does (code should be self-explanatory)

### Function Documentation

```typescript
/**
 * Calculates the total price including tax and discounts.
 *
 * @param items - Line items to calculate
 * @param taxRate - Tax rate as decimal (e.g., 0.08 for 8%)
 * @param discountCode - Optional discount code to apply
 * @returns Total price in cents
 * @throws InvalidDiscountError if discount code is invalid
 */
function calculateTotal(items: LineItem[], taxRate: number, discountCode?: string): number {
  // Implementation
}
```

## Integration & Consumer Documentation

### Export Consumer Requirements

- Every new exported service or class must document its intended consumer(s) in
  a JSDoc `@consumers` tag or inline comment
- Services without consumers must be marked with a tracking comment:
  `// TODO(#NNN): Wire to [consumer]`
- Prefer wiring consumer code in the same PR over deferring to a future PR

### Example

```typescript
/**
 * Analyzes pipeline execution history for health insights.
 *
 * @consumers PostPipelineAnalyzer — triggers auto-remediation on score < 70
 * @consumers DashboardHealthView — displays score in sidebar
 */
export class HealthAnalysisEngine {
  analyze(history: ExecutionHistory[]): HealthAnalysisResult { ... }
}
```

## Testing Standards

### Test Structure

```typescript
describe("UserService", () => {
  describe("getById", () => {
    it("should return user when found", async () => {
      // Arrange
      const userId = "test-123";
      const expectedUser = { id: userId, name: "Test User" };

      // Act
      const result = await userService.getById(userId);

      // Assert
      expect(result).toEqual(expectedUser);
    });

    it("should throw NotFoundError when user does not exist", async () => {
      // Arrange
      const userId = "nonexistent";

      // Act & Assert
      await expect(userService.getById(userId)).rejects.toThrow(NotFoundError);
    });
  });
});
```

### Coverage Expectations

- New code: 80% minimum coverage
- Critical paths: 100% coverage
- Focus on meaningful tests over coverage numbers

## External API & Dependency Usage

### Never Assume — Always Verify

Before implementing client-side workarounds for perceived API limitations,
**verify the API's actual capabilities** by consulting the latest documentation
or introspecting the schema. Assumptions about what an API can't do lead to:

- Unnecessary complexity (client-side filtering, caching layers, deduplication)
- Performance problems (fetching 677 items to filter 15 locally)
- Race conditions and bugs that only exist because of the workaround

### Required Steps Before API Integration

1. **Read the latest official documentation** — not blog posts, not old examples
2. **Introspect the API schema** when available (GraphQL `__type` queries,
   OpenAPI specs, CLI `--help`)
3. **Test the API directly** before writing integration code (use `curl`,
   `gh api`, or a playground)
4. **Prefer server-side operations** — filtering, sorting, pagination should
   happen on the server whenever the API supports it

### Example: GitHub ProjectV2 Server-Side Filtering

GitHub's `ProjectV2.items` connection accepts a `query` parameter for
server-side filtering (`items(query: "status:Ready is:open")`), but this isn't
prominently documented. We initially assumed filtering had to be done
client-side, leading to fetching all 677 items across 7 paginated API calls,
filtering locally, and introducing a progressive-rendering race condition that
caused empty views on refresh. A single GraphQL introspection query would have
revealed the `query` parameter immediately.

```bash
# Always introspect before assuming
gh api graphql -f query='{ __type(name: "ProjectV2") {
  fields { name args { name type { name } } }
} }'
```

## Security Standards

### Input Validation

- Validate all external input (user input, API responses, file uploads)
- Use allowlists over blocklists
- Sanitize before use, escape before output

### Secrets Management

- Never hardcode secrets
- Use environment variables or secret managers
- Never log secrets

### Database Security

- Always use parameterized queries
- Apply principle of least privilege
- Encrypt sensitive data at rest

## Git Practices

### Commit Messages

```text
type(scope): brief description

Longer explanation if needed. Explain WHY, not WHAT.

Refs: TICKET-123
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`

### Branch Naming

- Feature: `feature/TICKET-123-brief-description`
- Bugfix: `bugfix/TICKET-123-brief-description`
- Hotfix: `hotfix/TICKET-123-brief-description`

---

**Source:** [Nightgauge](https://github.com/nightgauge/nightgauge)
