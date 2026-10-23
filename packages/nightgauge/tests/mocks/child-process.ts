/**
 * A `ChildProcess` double for the skill-runner tests.
 *
 * Twelve test files carried a byte-identical copy of this factory (two of
 * them with `as any` where the other ten had a wrong-but-quiet cast). None
 * typechecked, because the test tree never did (#499).
 *
 * The casts that remain are unavoidable and narrow: `ChildProcess.stdout` is
 * `Readable | null` and `stdin` is `Writable | null` — real stream classes
 * with dozens of members — while these tests only ever `emit("data", …)` and
 * assert on `write`. An EventEmitter is the right double; it simply cannot
 * structurally BE a Readable. `killed` is `readonly` on the interface, so it
 * is seeded through the object rather than assigned after the fact.
 */
import { EventEmitter } from "events";
import { vi } from "vitest";
import type { ChildProcess } from "child_process";
import type { Readable, Writable } from "stream";

export interface MockChildProcess extends ChildProcess {
  stdout: Readable;
  stderr: Readable;
  stdin: Writable;
}

export function createMockChildProcess(): MockChildProcess {
  const proc = new EventEmitter() as MockChildProcess;
  proc.stdout = new EventEmitter() as unknown as Readable;
  proc.stderr = new EventEmitter() as unknown as Readable;
  proc.stdin = {
    write: vi.fn(),
    end: vi.fn(),
    destroyed: false,
  } as unknown as Writable;
  proc.kill = vi.fn();
  // `killed` is declared readonly; defineProperty is how a double seeds it
  // without the compiler rejecting the assignment.
  Object.defineProperty(proc, "killed", { value: false, writable: true });
  return proc;
}
