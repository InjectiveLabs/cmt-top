export type VoteKind = "voted" | "nil" | "zero" | "absent";
export interface Vote {
  kind: VoteKind;
  blockIDHash: string;
}
export interface Validator {
  address: string;
  index: number;
  votingPower: string;
  votingPowerPercent: number;
  prevote: Vote;
  precommit: Vote;
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
export interface Upgrade {
  name: string;
  height: number;
}
export interface Health {
  mode: "streaming" | "polling" | "stale" | "unavailable";
  wsConnected: boolean;
  wsEndpoint: string;
  httpEndpoint: string;
  lastEventAt?: string;
  lastSuccessAt?: string;
  lastError?: string;
  staleAfterMs: number;
}
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
export interface Divergence {
  live: DivergenceRound[];
  history: DivergenceRound[];
}
export interface BlockSample {
  height: number;
  time: string;
  blockTimeMs?: number;
  blockIDHash?: string;
  appHash?: string;
  numTxs?: number;
}
export interface RPCEndpoint {
  endpoint: string;
  status: "reference" | "match" | "mismatch" | "lagging" | "chain_mismatch" | "error";
  chainId: string;
  height: number;
  latestHeight: number;
  appHash: string;
  error?: string;
}
export interface RPCComparison {
  status: "not_configured" | "matching" | "mismatch" | "incomplete";
  height: number;
  chainId: string;
  checkedAt?: string;
  endpoints: RPCEndpoint[];
}
export interface Snapshot {
  height: number;
  committedHeight: number;
  round: number;
  step: number;
  startTime?: string;
  blockTime: number;
  activeRPC: string;
  chain: ChainInfo;
  upgrade: Upgrade | null;
  validators: Validator[];
  divergence: Divergence;
  errors: Record<string, string>;
  health: Health;
  blocks: BlockSample[];
  rpcComparison: RPCComparison;
  explorerURL: string;
  receivedAt: number;
  displayName?: string;
}
export interface Envelope {
  type: string;
  ts?: string;
  height?: number;
  round?: number;
  seq: number;
  payload: unknown;
}
