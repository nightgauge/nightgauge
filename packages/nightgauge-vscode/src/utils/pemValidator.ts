import { readFile } from "fs/promises";

/**
 * Validates that a file at the given path contains at least one PEM certificate
 * (i.e. starts with a `-----BEGIN CERTIFICATE-----` header).
 *
 * Returns null on success, or an error string on failure.
 * Only validates the header line — deep X.509 parsing is deferred to Go.
 */
export async function validatePemFile(fsPath: string): Promise<string | null> {
  let contents: string;
  try {
    const buf = await readFile(fsPath);
    contents = buf.toString("utf8");
  } catch (err) {
    return `Cannot read file: ${err instanceof Error ? err.message : String(err)}`;
  }

  if (!contents.includes("-----BEGIN CERTIFICATE-----")) {
    return "File does not appear to be a PEM certificate bundle (missing -----BEGIN CERTIFICATE----- header)";
  }

  return null;
}
