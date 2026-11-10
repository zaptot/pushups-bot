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

const countMs = 700;
const flashMs = 900;
const format = new Intl.NumberFormat('ru-RU');
const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)');

let shown: number | null = null;
let frame = 0;
let flashTimer = 0;

const paint = (n: number | null): void => {
  if (node) {
    node.textContent = n === null ? '…' : format.format(n);
  }
};

const flash = (kind: 'up' | 'down'): void => {
  if (!node) {
    return;
  }
  node.classList.remove('up', 'down');
  node.classList.add(kind);
  clearTimeout(flashTimer);
  flashTimer = window.setTimeout(() => node?.classList.remove(kind), flashMs);
};

/** Показывает value: при изменении число досчитывает до нового и подсвечивается. */
const render = (): void => {
  cancelAnimationFrame(frame);
  const from = shown;
  const to = value;
  shown = to;
  if (from === null || to === null || from === to || reducedMotion.matches) {
    paint(to);
    return;
  }
  flash(to > from ? 'up' : 'down');
  const t0 = performance.now();
  const step = (now: number): void => {
    const t = Math.min(1, (now - t0) / countMs);
    paint(Math.round(from + (to - from) * (1 - (1 - t) ** 3)));
    if (t < 1) {
      frame = requestAnimationFrame(step);
    }
  };
  frame = requestAnimationFrame(step);
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
