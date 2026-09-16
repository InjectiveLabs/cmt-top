import { describe, expect, it } from "vitest";
import { parseHeight, parseRoute, roundsPath, shouldNavigate } from "./routes";

describe("block investigation routes", () => {
  it("round trips pinned heights for reload and browser back/forward", () => {
    expect(parseRoute(roundsPath(1001))).toEqual({ page: "rounds", height: 1001 });
    expect(parseRoute("/")).toEqual({ page: "dashboard" });
    expect(parseRoute("/blocks/1001/rounds/")).toEqual({ page: "rounds", height: 1001 });
    expect(parseRoute("/blocks/1002/rounds")).toEqual({ page: "rounds", height: 1002 });
    expect(parseRoute("/blocks/1001/rounds")).toEqual({ page: "rounds", height: 1001 });
  });
  it("rejects ambiguous and unsafe heights", () => {
    for (const input of [
      "0",
      "-1",
      "1.5",
      "01",
      "1e3",
      "1x",
      "1/rounds",
      "9007199254740992",
      "Infinity",
      "",
    ])
      expect(parseHeight(input)).toBeNull();
    expect(parseHeight("9007199254740991")).toBe(9007199254740991);
    expect(parseRoute("/blocks/0/rounds")).toEqual({ page: "dashboard" });
  });
  it("leaves modified clicks to the browser", () => {
    const event = {
      defaultPrevented: false,
      button: 0,
      ctrlKey: false,
      metaKey: false,
      shiftKey: false,
      altKey: false,
    } as MouseEvent;
    expect(shouldNavigate(event)).toBe(true);
    expect(shouldNavigate({ ...event, metaKey: true })).toBe(false);
    expect(shouldNavigate({ ...event, button: 1 })).toBe(false);
  });
});
