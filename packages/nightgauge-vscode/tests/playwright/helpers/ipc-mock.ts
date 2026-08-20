/**
 * ipc-mock.ts — Complete IPC client mock factory with call tracking (Issue #2749).
 *
 * Creates a serializable mock of the IPC client that can be injected into a
 * Playwright page via page.evaluate(). All methods return pre-configured
 * responses. Call history is stored in window.__ipcMockCalls for assertions.
 *
 * Usage:
 *   const mock = createIpcMock({ boardList: { items: [...] } });
 *   await page.addInitScript(mock.initScript);
 *   // after interaction:
 *   const calls = await mock.getCalls(page);
 */

import { type Page } from "@playwright/test";

// ---------------------------------------------------------------------------
// Types matching IpcClientBase interfaces
// ---------------------------------------------------------------------------

export interface MockBoardItem {
  id: string;
  number: number;
  title: string;
  state: string;
  status: string;
  priority: string;
  size: string;
  labels: string[];
  assignees: string[];
  repo: string;
  url: string;
  isEpic: boolean;
  parentIssueNumber?: number;
  parentIssueTitle?: string;
  blockedBy?: Array<{ number: number; title: string; state: string; repo?: string }>;
  blocking?: Array<{ number: number; title: string; state: string; repo?: string }>;
  subIssues?: Array<{ number: number; title: string; state: string; repo?: string }>;
}

export interface MockStatusCounts {
  ready: number;
  inProgress: number;
  inReview: number;
  done: number;
  backlog: number;
}

export interface MockPipelineStatus {
  executionId: string;
  issueNumber: number;
  status: string;
  stage?: string;
  progress?: number;
  error?: string;
}

export interface MockConfigProject {
  owner: string;
  repo: string;
  projectNumber: number;
  defaultBranch?: string;
}

export interface MockIpcOptions {
  /** Response for boardList() — defaults to empty array */
  boardItems?: MockBoardItem[];
  /** Response for boardCounts() — defaults to all zeros */
  boardCounts?: MockStatusCounts;
  /** Response for configGetProjectConfig() */
  projectConfig?: MockConfigProject;
  /** Response for pipelineStatus() */
  pipelineStatus?: MockPipelineStatus;
  /**
   * When set, the named methods will reject with this error on the FIRST call.
   * Subsequent calls use the normal response.
   * key: method name, value: error message
   */
  rejectOnce?: Partial<Record<string, string>>;
  /**
   * When set, all calls to the named method will be delayed by this many ms.
   * key: method name, value: delay in ms
   */
  delays?: Partial<Record<string, number>>;
}

export interface IpcCall {
  method: string;
  args: unknown[];
  returnedAt: number;
}

// ---------------------------------------------------------------------------
// Mock factory
// ---------------------------------------------------------------------------

/**
 * Creates an IPC client mock configuration.
 * Call `initScript` property value to inject into Playwright page.
 * Call `getCalls(page)` after interactions to assert call history.
 */
export function createIpcMock(options: MockIpcOptions = {}) {
  const serialized = JSON.stringify(options);

  /**
   * The init script is injected into the page context via page.addInitScript.
   * It sets up window.__ipcMock with all methods and call tracking.
   */
  const initScript = `(function() {
    var opts = ${serialized};
    var calls = [];
    var rejectOnceUsed = {};

    function delay(ms) {
      return new Promise(function(resolve) { setTimeout(resolve, ms || 0); });
    }

    function trackCall(method, args) {
      calls.push({ method: method, args: args, returnedAt: Date.now() });
    }

    function shouldRejectOnce(method) {
      if (opts.rejectOnce && opts.rejectOnce[method] && !rejectOnceUsed[method]) {
        rejectOnceUsed[method] = true;
        return opts.rejectOnce[method];
      }
      return null;
    }

    async function mockMethod(method, args, responseFn) {
      var delayMs = opts.delays && opts.delays[method] || 0;
      if (delayMs > 0) await delay(delayMs);
      var err = shouldRejectOnce(method);
      if (err) {
        trackCall(method, args);
        throw new Error(err);
      }
      var result = await responseFn();
      trackCall(method, args);
      return result;
    }

    window.__ipcMock = {
      boardList: function(owner, projectNumber, status) {
        return mockMethod('boardList', [owner, projectNumber, status], function() {
          return Promise.resolve(opts.boardItems || []);
        });
      },
      boardCounts: function(owner, projectNumber) {
        return mockMethod('boardCounts', [owner, projectNumber], function() {
          return Promise.resolve(opts.boardCounts || { ready: 0, inProgress: 0, inReview: 0, done: 0, backlog: 0 });
        });
      },
      configGetProjectConfig: function(root) {
        return mockMethod('configGetProjectConfig', [root], function() {
          return Promise.resolve(opts.projectConfig || { owner: 'nightgauge', repo: 'nightgauge', projectNumber: 1 });
        });
      },
      pipelineStatus: function(owner, projectNumber, itemId) {
        return mockMethod('pipelineStatus', [owner, projectNumber, itemId], function() {
          return Promise.resolve(opts.pipelineStatus || { executionId: '', issueNumber: 0, status: 'idle' });
        });
      },
      pipelineStop: function(executionId) {
        return mockMethod('pipelineStop', [executionId], function() {
          return Promise.resolve({ status: 'stopped' });
        });
      },
      pipelinePause: function(executionId) {
        return mockMethod('pipelinePause', [executionId], function() {
          return Promise.resolve();
        });
      },
      pipelineResume: function(executionId) {
        return mockMethod('pipelineResume', [executionId], function() {
          return Promise.resolve();
        });
      },
      on: function(event, handler) {
        if (!window.__ipcMockEventHandlers) window.__ipcMockEventHandlers = {};
        if (!window.__ipcMockEventHandlers[event]) window.__ipcMockEventHandlers[event] = [];
        window.__ipcMockEventHandlers[event].push(handler);
        return { dispose: function() {} };
      },
      call: function(method, args) {
        return mockMethod(method, [args], function() {
          return Promise.resolve({ ok: true });
        });
      },
      getCalls: function() { return calls; },
    };

    // Expose call-tracking globally
    window.__ipcMockCalls = calls;
  })();`;

  return {
    initScript,
    /** Retrieve all recorded IPC calls from the page. */
    async getCalls(page: Page): Promise<IpcCall[]> {
      return page.evaluate(() => (window as any).__ipcMockCalls ?? []);
    },
    /** Retrieve calls matching a specific method name. */
    async getCallsFor(page: Page, method: string): Promise<IpcCall[]> {
      const all = await page.evaluate(() => (window as any).__ipcMockCalls ?? []);
      return (all as IpcCall[]).filter((c) => c.method === method);
    },
    /**
     * Emit a mock IPC event to all registered handlers.
     * Use this to simulate pipeline progress events after interactions.
     */
    async emitEvent(page: Page, event: string, data: unknown): Promise<void> {
      await page.evaluate(
        ([evt, payload]) => {
          const handlers = (window as any).__ipcMockEventHandlers?.[evt] ?? [];
          for (const h of handlers) h(payload);
        },
        [event, data] as [string, unknown]
      );
    },
  };
}
