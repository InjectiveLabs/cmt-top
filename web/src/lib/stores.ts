import { writable, type Writable } from "svelte/store";

export interface Validator {
  address: string;
  index: number;
  votingPower: string;
  votingPowerPercent: number;
  prevote: { kind: string; blockIDHash: string };
  precommit: { kind: string; blockIDHash: string };
  isProposer: boolean;
  operatorAddress?: string;
  moniker?: string;
  jailed?: boolean;
  active?: boolean;
  commissionRate?: string;
}

export interface ChainInfo {
  network?: string;
  cometVersion?: string;
  ourValidator?: string;
  catchingUp?: boolean;
}

export interface Upgrade { name: string; height: number; }

export interface DivergenceGroup {
  BlockIDHash: string;
  VotingPower: number;
  VotingPowerPct: number;
  ValidatorCount: number;
  SampleMonikers: string[];
  Validators: string[];
  IsCanonical: boolean;
}

export interface DivergenceRound {
  Height: number;
  Round: number;
  Type: number;
  Groups: DivergenceGroup[];
  TotalVotingPower: number;
  TotalVotedPower: number;
  Resolved: boolean;
  CanonicalHash: string;
  IsDivergent: boolean;
}

export interface BlockSample { height: number; time: string; blockTimeMs?: number; }

export const chain: Writable<{ height: number; round: number; step: number; blockTime?: number; activeRPC?: string; chain?: ChainInfo; upgrade?: Upgrade | null; }> = writable({ height: 0, round: 0, step: 0 });
export const validators: Writable<Map<string, Validator>> = writable(new Map());
export const validatorsOrdered: Writable<Validator[]> = writable([]);
export const divergence: Writable<{ live: DivergenceRound[]; history: DivergenceRound[] }> = writable({ live: [], history: [] });
export const blocks: Writable<BlockSample[]> = writable([]);
export const errors: Writable<Record<string, string>> = writable({});

export function applySnapshot(snap: any) {
  chain.set({
    height: snap.height ?? 0,
    round: snap.round ?? 0,
    step: snap.step ?? 0,
    blockTime: snap.blockTime,
    activeRPC: snap.activeRPC,
    chain: snap.chain,
    upgrade: snap.upgrade ?? null,
  });
  const m = new Map<string, Validator>();
  const order: Validator[] = [];
  for (const v of (snap.validators ?? []) as Validator[]) {
    m.set(v.address, v);
    order.push(v);
  }
  validators.set(m);
  validatorsOrdered.set(order);
  if (snap.divergence) divergence.set(snap.divergence);
  errors.set(snap.errors ?? {});
}

export function applyVote(p: any) {
  const addr = p.ValidatorAddr ?? p.validatorAddr;
  if (!addr) return;
  const slot = p.Type === 1 || p.Type === "prevote" ? "prevote" : "precommit";
  const hash = p.BlockIDHash ?? p.blockIDHash ?? "";
  const cell = { kind: hash ? "voted" : "nil", blockIDHash: hash };
  validators.update((m) => {
    const v = m.get(addr);
    if (!v) return m;
    const next = new Map(m);
    next.set(addr, { ...v, [slot]: cell });
    return next;
  });
  // ValidatorTable renders from validatorsOrdered; keep both in sync.
  validatorsOrdered.update((arr) => {
    let touched = false;
    const out = arr.map((v) => {
      if (v.address !== addr) return v;
      touched = true;
      return { ...v, [slot]: cell };
    });
    return touched ? out : arr;
  });
}

export function applyChain(p: any) {
  chain.update((c) => ({
    ...c,
    height: p.Height ?? p.height ?? c.height,
    round: p.Round ?? p.round ?? c.round,
    step: p.Step ?? p.step ?? c.step,
  }));
}

// resetRoundVotes wipes vote/proposer state on every validator. Called on
// round.changed and block.committed; the server has already done the same so
// the next vote.received frames repopulate consistently.
export function resetRoundVotes() {
  validators.update((m) => {
    const next = new Map<string, Validator>();
    for (const [addr, v] of m) {
      next.set(addr, {
        ...v,
        prevote: { kind: "absent", blockIDHash: "" },
        precommit: { kind: "absent", blockIDHash: "" },
        isProposer: false,
      });
    }
    return next;
  });
  validatorsOrdered.update((arr) =>
    arr.map((v) => ({
      ...v,
      prevote: { kind: "absent", blockIDHash: "" },
      precommit: { kind: "absent", blockIDHash: "" },
      isProposer: false,
    })),
  );
}

// applyProposer marks one validator as the round's proposer (and clears the
// flag on everyone else).
export function applyProposer(addr: string) {
  if (!addr) return;
  const target = addr.toUpperCase();
  validators.update((m) => {
    const next = new Map<string, Validator>();
    for (const [a, v] of m) {
      next.set(a, { ...v, isProposer: a === target });
    }
    return next;
  });
  validatorsOrdered.update((arr) => arr.map((v) => ({ ...v, isProposer: v.address === target })));
}

// applyDivergenceSnapshot replaces the full live/history view with a fresh
// tracker snapshot. Pushed after every block commit so the panel stays current
// for healthy rounds (which never trigger a divergence.detected event).
export function applyDivergenceSnapshot(p: any) {
  if (!p) return;
  divergence.set({
    live: (p.live ?? p.Live ?? []) as DivergenceRound[],
    history: (p.history ?? p.History ?? []) as DivergenceRound[],
  });
}

export function applyDivergenceLive(rep: any) {
  divergence.update((d) => {
    const idx = d.live.findIndex((r) => r.Height === rep.Height && r.Round === rep.Round && r.Type === rep.Type);
    const next = [...d.live];
    if (idx >= 0) next[idx] = rep;
    else next.push(rep);
    return { ...d, live: next };
  });
}

export function applyDivergenceResolved(rep: any) {
  divergence.update((d) => ({
    live: d.live.filter((r) => r.Height !== rep.Height || r.Round !== rep.Round || r.Type !== rep.Type),
    history: [rep, ...d.history].slice(0, 50),
  }));
}

export function applyBlock(p: any) {
  const sample: BlockSample = {
    height: p.Height ?? p.height,
    time: p.Time ?? p.time,
  };
  blocks.update((bs) => {
    const next = [...bs, sample];
    if (next.length > 200) next.shift();
    return next;
  });
}
