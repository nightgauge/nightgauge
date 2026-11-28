import { describe, expect, it } from "vitest";
import { type PanelViewState, viewStateLine } from "./panelViewState";

const s = (
  at: number,
  cause: PanelViewState["cause"],
  visible: boolean,
  active = false
): PanelViewState => ({ at, cause, visible, active });

describe("viewStateLine", () => {
  it("does not take the host's visible-at-creation default as evidence", () => {
    const line = viewStateLine([s(100, "created", true, true), s(150, "html", true)], 100);
    expect(line).toContain("the workbench sent no view-state change");
    expect(line).toContain("host default at creation visible+active");
    expect(line).toContain("not a layout report");
  });

  it("reports a panel the workbench hid, with the change offset", () => {
    const line = viewStateLine([s(100, "created", true), s(400, "viewState", false)], 100);
    expect(line).toContain("now hidden;");
    expect(line).toContain("workbench changes hidden@+300ms");
  });

  it("lists every workbench change in order and the current state", () => {
    const line = viewStateLine(
      [
        s(100, "created", true),
        s(200, "viewState", true, true),
        s(900, "html", true),
        s(1100, "viewState", false),
      ],
      100
    );
    expect(line).toContain("now hidden;");
    expect(line).toContain("workbench changes visible+active@+100ms, hidden@+1000ms");
  });

  it("says when no creation sample exists", () => {
    expect(viewStateLine([], 0)).toContain("no creation sample");
  });
});
