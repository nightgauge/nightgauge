/**
 * AsyncMutex - Simple Promise-based mutex for serializing async operations
 *
 * Provides exclusive access to a resource within a single process. This is used
 * by PipelineStateService to prevent concurrent writes to state.json.
 *
 * @see Issue #414 - Harden Pipeline State Management
 *
 * @example
 * ```typescript
 * const mutex = new AsyncMutex();
 *
 * // Using runExclusive (recommended)
 * const result = await mutex.runExclusive(async () => {
 *   // This code runs exclusively
 *   return await doSomething();
 * });
 *
 * // Or manual acquire/release
 * const release = await mutex.acquire();
 * try {
 *   await doSomething();
 * } finally {
 *   release();
 * }
 * ```
 */
export class AsyncMutex {
  private queue: Array<() => void> = [];
  private locked = false;

  /**
   * Acquire the mutex lock
   *
   * Returns a release function that must be called when done.
   * If the mutex is already locked, this will wait until it's available.
   */
  acquire(): Promise<() => void> {
    return new Promise((resolve) => {
      const tryAcquire = () => {
        if (!this.locked) {
          this.locked = true;
          resolve(() => this.release());
        } else {
          this.queue.push(tryAcquire);
        }
      };
      tryAcquire();
    });
  }

  /**
   * Release the mutex lock
   *
   * Allows the next queued operation to proceed.
   */
  private release(): void {
    this.locked = false;
    const next = this.queue.shift();
    if (next) {
      next();
    }
  }

  /**
   * Run a function with exclusive access
   *
   * Automatically acquires and releases the lock. This is the recommended
   * way to use the mutex as it guarantees release even if the function throws.
   *
   * @param fn - The async function to run exclusively
   * @returns The result of the function
   */
  async runExclusive<T>(fn: () => Promise<T>): Promise<T> {
    const release = await this.acquire();
    try {
      return await fn();
    } finally {
      release();
    }
  }

  /**
   * Check if the mutex is currently locked
   *
   * Useful for debugging and testing.
   */
  isLocked(): boolean {
    return this.locked;
  }

  /**
   * Get the number of waiting operations
   *
   * Useful for debugging and testing.
   */
  getQueueLength(): number {
    return this.queue.length;
  }
}
