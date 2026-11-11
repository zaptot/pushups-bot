export const inDiscord = new URLSearchParams(location.search).has('frame_id');

const base = inDiscord ? '/.proxy/api' : '/api';
let token = '';

export interface AppConfig {
  clientId: string;
  dev: boolean;
}

export interface Session {
  citizen: boolean;
  token?: string;
  user: { id: string; name: string };
}

/** Что ставка сделала с джекпотом: пул после неё, сколько добавила, сколько сорвал игрок. */
export interface JackpotOutcome {
  pool: number;
  added: number;
  won?: number;
}

export interface JackpotWin {
  name: string;
  game: string;
  amount: number;
  at: string;
}

export interface JackpotState {
  pool: number;
  divisor: number;
  target: number;
  last: JackpotWin | null;
}

export interface Settled {
  multiplier: number;
  payout: number;
  jackpot?: JackpotOutcome;
}

export type RouletteBetKind = 'red' | 'black' | 'even' | 'odd' | 'low' | 'high' | 'dozen' | 'number';

export interface RouletteBet {
  kind: RouletteBetKind;
  value?: number;
}

export interface RouletteResult extends Settled {
  number: number;
}

export type PlinkoRisk = 'low' | 'medium' | 'high';

export type PlinkoTables = Record<string, Record<PlinkoRisk, number[]>>;

export interface PlinkoResult extends Settled {
  rows: number;
  risk: PlinkoRisk;
  path: number[];
  slot: number;
}

export type BlackjackStatus = 'playing' | 'blackjack' | 'win' | 'push' | 'lose' | 'bust';

export type BlackjackAction = 'hit' | 'stand' | 'double';

export interface BlackjackState extends Settled {
  id: string;
  seq: number;
  stake: number;
  player: string[];
  dealer: string[];
  playerTotal: number;
  dealerTotal: number;
  status: BlackjackStatus;
  canDouble: boolean;
}

export interface RocketStart {
  id: string;
  growth: number;
  maxMultiplier: number;
}

export interface RocketResult extends Settled {
  status: 'won' | 'crashed';
  crash: number;
}

export type RocketPoll = { status: 'flying' } | RocketResult;

interface ErrorBody {
  error: string;
  code?: string;
}

/** Код ответа, когда поинтов не хватает на ставку или покупку. */
export const notEnoughCode = 'not_enough';

let notEnoughHandler: (() => void) | null = null;

/** Что показать, когда сервер ответил, что поинтов не хватает. */
export const onNotEnough = (fn: () => void): void => {
  notEnoughHandler = fn;
};

const isErrorBody = (v: unknown): v is ErrorBody =>
  typeof v === 'object' && v !== null && typeof (v as ErrorBody).error === 'string';

export const setToken = (value: string): void => {
  token = value;
};

export async function request<T>(method: 'GET' | 'POST', path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const res = await fetch(base + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data: unknown = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) {
    if (isErrorBody(data) && data.code === notEnoughCode) {
      notEnoughHandler?.();
    }
    throw new Error(isErrorBody(data) ? data.error : `Ошибка сервера (${res.status})`);
  }
  return data as T;
}
