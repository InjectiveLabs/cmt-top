export type AppRoute = { page: "dashboard" } | { page: "rounds"; height: number };

export function parseHeight(value: string): number | null {
  if (!/^[1-9]\d*$/.test(value)) return null;
  const height = Number(value);
  return Number.isSafeInteger(height) ? height : null;
}

export function roundsPath(height: number): string {
  return `/blocks/${height}/rounds`;
}

export function parseRoute(path: string): AppRoute {
  const match = /^\/blocks\/([1-9]\d*)\/rounds\/?$/.exec(path);
  const height = match && parseHeight(match[1]);
  return height ? { page: "rounds", height } : { page: "dashboard" };
}

export function shouldNavigate(event: MouseEvent): boolean {
  return (
    !event.defaultPrevented &&
    event.button === 0 &&
    !event.ctrlKey &&
    !event.metaKey &&
    !event.shiftKey &&
    !event.altKey
  );
}
