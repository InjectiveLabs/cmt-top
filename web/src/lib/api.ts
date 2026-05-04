export async function fetchSnapshot(token?: string) {
  const headers: Record<string, string> = {};
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const r = await fetch("/api/state", { headers });
  if (!r.ok) throw new Error(`/api/state ${r.status}`);
  return r.json();
}

export async function fetchValidators(search?: string, token?: string) {
  const headers: Record<string, string> = {};
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const u = new URL("/api/validators", location.origin);
  if (search) u.searchParams.set("search", search);
  const r = await fetch(u.toString(), { headers });
  if (!r.ok) throw new Error(`/api/validators ${r.status}`);
  return r.json();
}
