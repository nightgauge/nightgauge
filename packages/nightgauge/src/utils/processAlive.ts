/**
 * Whether the process `pid` is alive on this machine. A signal-0 probe: EPERM
 * means the process exists under another user. 0 and negative ids are never
 * alive, so a record that names no owner refuses nothing.
 */
export function isProcessAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return (err as NodeJS.ErrnoException).code === "EPERM";
  }
}
