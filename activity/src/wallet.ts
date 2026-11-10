import { request } from './api';

let node: HTMLElement | null = null;
let value: number | null = null;

/** 1 поинт, 2 поинта, 5 поинтов. */
export function points(n: number): string {
  const a = Math.abs(n);
  let word = 'поинтов';
  if (a % 100 < 11 || a % 100 > 14) {
    if (a % 10 === 1) {
      word = 'поинт';
    } else if (a % 10 >= 2 && a % 10 <= 4) {
      word = 'поинта';
    }
  }
  return `${n} ${word}`;
}

const render = (): void => {
  if (node) {
    node.textContent = value === null ? '💰 …' : `💰 ${value}`;
  }
};

/** Где показывать баланс (в шапке). */
export function showBalanceIn(target: HTMLElement): void {
  node = target;
  render();
}

export function setBalance(n: number): void {
  value = n;
  render();
}

/** Сразу показать списанную ставку, не раскрывая итог до конца анимации. */
export function spend(n: number): void {
  if (value !== null) {
    value -= n;
    render();
  }
}

export async function refreshBalance(): Promise<void> {
  try {
    const res = await request<{ balance: number }>('GET', '/balance');
    setBalance(res.balance);
  } catch (e) {
    console.log({ balanceError: e instanceof Error ? e.message : e });
  }
}
