/**
 * A complete `ITokenStorage` double.
 *
 * Four test files each hand-built a three-method stub — `retrieve`, `store`,
 * `delete` — and cast it at the call site. `ITokenStorage` has seven members;
 * the other four (`clear`, `notifyHostChanged`, `onTokenChanged`, `dispose`)
 * were simply absent, and nothing noticed because the test tree was never
 * typechecked (#499).
 *
 * That is not a hypothetical gap: a service that starts calling `clear()` or
 * subscribing to `onTokenChanged` would throw at runtime in exactly the tests
 * meant to cover it. Building the double from the interface means adding a
 * member to `ITokenStorage` breaks this one file, loudly, instead of four
 * files silently.
 */
import { vi } from "vitest";
import type { ITokenStorage } from "../../src/platform/TokenStorage";

export interface MockTokenStorage extends ITokenStorage {
  retrieve: ReturnType<typeof vi.fn<ITokenStorage["retrieve"]>>;
  store: ReturnType<typeof vi.fn<ITokenStorage["store"]>>;
  delete: ReturnType<typeof vi.fn<ITokenStorage["delete"]>>;
  clear: ReturnType<typeof vi.fn<ITokenStorage["clear"]>>;
  notifyHostChanged: ReturnType<typeof vi.fn<ITokenStorage["notifyHostChanged"]>>;
  dispose: ReturnType<typeof vi.fn<ITokenStorage["dispose"]>>;
}

/**
 * @param token what `retrieve` resolves to; `null` models "not signed in".
 */
export function makeMockTokenStorage(token: string | null = "test-token"): MockTokenStorage {
  return {
    retrieve: vi.fn<ITokenStorage["retrieve"]>().mockResolvedValue(token),
    store: vi.fn<ITokenStorage["store"]>().mockResolvedValue(undefined),
    delete: vi.fn<ITokenStorage["delete"]>().mockResolvedValue(undefined),
    clear: vi.fn<ITokenStorage["clear"]>().mockResolvedValue(undefined),
    notifyHostChanged: vi.fn<ITokenStorage["notifyHostChanged"]>(),
    // A no-op subscription: returns a disposable, never fires. A test that
    // needs an event drives it by replacing this.
    onTokenChanged: () => ({ dispose: () => {} }),
    dispose: vi.fn<ITokenStorage["dispose"]>(),
  };
}
