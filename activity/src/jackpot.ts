// Джекпот: пул, который растёт с проигрышей и срывается с любой ставки.
// Шанс со ставки = ставка × пул / divisor (считает и сервер, здесь — только для подсказки).
import { request, type JackpotOutcome, type JackpotState } from './api';
import { showOverlay } from './overlay';
import { points, refreshBalance } from './wallet';

const pollMs = 10_000;
const countMs = 700;
const confettiCount = 48;
const format = new Intl.NumberFormat('ru-RU');
const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)');

const gameNames: Record<string, string> = {
  roulette: 'в рулетке',
  plinko: 'в Plinko',
  blackjack: 'в блэкджеке',
  rocket: 'в рокетке',
};

let state: JackpotState | null = null;
let pollTimer = 0;
const listeners = new Set<(added: number) => void>();

const make = <K extends keyof HTMLElementTagNameMap>(tag: K, className: string, text = ''): HTMLElementTagNameMap[K] => {
  const node = document.createElement(tag);
  node.className = className;
  node.textContent = text;
  return node;
};

const notify = (added = 0): void => {
  for (const fn of listeners) {
    fn(added);
  }
};

/** Подписка на изменения пула; слушатель сам отпишется, когда его узел уберут со страницы. */
function watch(node: HTMLElement, fn: (added: number) => void): void {
  const listener = (added: number): void => {
    if (!node.isConnected) {
      listeners.delete(listener);
      return;
    }
    fn(added);
  };
  listeners.add(listener);
}

export async function refreshJackpot(): Promise<void> {
  try {
    state = await request<JackpotState>('GET', '/jackpot');
    notify();
  } catch (e) {
    console.log({ jackpotError: e instanceof Error ? e.message : e });
  }
}

/** Опрашивать пул, пока страница открыта: его пополняют и другие игроки. */
export function startJackpotPolling(): void {
  clearInterval(pollTimer);
  void refreshJackpot();
  pollTimer = window.setInterval(() => {
    if (document.visibilityState === 'visible') {
      void refreshJackpot();
    }
  }, pollMs);
}

/** Учесть итог своей ставки: обновить пул, показать «+N в джекпот» или праздник срыва. */
export function applyJackpot(outcome: JackpotOutcome | undefined): void {
  if (!outcome) {
    return;
  }
  if (state) {
    state.pool = outcome.pool;
  }
  notify(outcome.added);
  if (outcome.won) {
    showJackpotWin(outcome.won);
    void refreshBalance();
    void refreshJackpot();
  }
}

function chance(stake: number): number {
  return state && state.pool > 0 ? Math.min(1, (stake * state.pool) / state.divisor) : 0;
}

function chanceText(stake: number | null): string {
  if (stake === null || !state) {
    return '';
  }
  const p = chance(stake);
  if (p <= 0) {
    return 'шанс появится, когда в пуле будут поинты';
  }
  return p >= 0.5 ? `шанс с этой ставки ${Math.round(p * 100)}%` : `шанс с этой ставки 1 к ${format.format(Math.round(1 / p))}`;
}

interface Heat {
  ratio: number;
  label: string;
}

function heat(): Heat {
  const ratio = state ? state.pool / state.target : 0;
  const label =
    ratio < 0.3 ? 'холодный 🧊' : ratio < 0.6 ? 'теплеет' : ratio < 0.9 ? 'горячий 🔥' : 'вот-вот рванёт 💥';
  return { ratio, label };
}

/** Число, которое досчитывает до нового значения. */
function counter(node: HTMLElement): (n: number) => void {
  let shown: number | null = null;
  let frame = 0;
  return (to: number): void => {
    cancelAnimationFrame(frame);
    const from = shown;
    shown = to;
    if (from === null || from === to || reducedMotion.matches) {
      node.textContent = format.format(to);
      return;
    }
    const t0 = performance.now();
    const step = (now: number): void => {
      const t = Math.min(1, (now - t0) / countMs);
      node.textContent = format.format(Math.round(from + (to - from) * (1 - (1 - t) ** 3)));
      if (t < 1) {
        frame = requestAnimationFrame(step);
      }
    };
    frame = requestAnimationFrame(step);
  };
}

function lastText(): string {
  const last = state?.last;
  if (!last) {
    return 'Джекпот ещё никто не срывал — будь первым';
  }
  const day = new Date(last.at).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long' });
  return `Последний: ${last.name} — ${points(last.amount)} ${gameNames[last.game] ?? ''}, ${day}`;
}

/** Баннер джекпота для меню: сумма, «нагрев» и последний победитель. */
export function jackpotBanner(): HTMLElement {
  const banner = make('section', 'jackpot');
  const head = make('div', 'jackpot-head');
  const amount = make('span', 'jackpot-amount', '…');
  head.append(make('span', 'jackpot-label', '💎 Джекпот'), amount);
  const bar = make('div', 'jackpot-bar');
  const cover = make('div', 'jackpot-cover');
  bar.append(cover);
  const heatLabel = make('span', 'jackpot-heat');
  const last = make('p', 'jackpot-last');
  banner.append(head, bar, heatLabel, last);
  const setAmount = counter(amount);

  const render = (): void => {
    if (!state) {
      return;
    }
    const h = heat();
    setAmount(state.pool);
    cover.style.width = `${(1 - Math.min(1, h.ratio)) * 100}%`;
    heatLabel.textContent = h.label;
    banner.classList.toggle('hot', h.ratio >= 0.9);
    last.textContent = lastText();
  };
  watch(banner, render);
  render();
  return banner;
}

/** Строка под ставкой: пул, шанс сорвать с этой ставки и всплывающее «+N в джекпот». */
export function jackpotLine(input: HTMLInputElement, read: () => number | null): HTMLElement {
  const line = make('p', 'jackpot-line');
  const text = make('span', '');
  line.append(text);
  const render = (): void => {
    if (!state) {
      text.textContent = '';
      return;
    }
    const hint = chanceText(read());
    text.textContent = `💎 Джекпот ${format.format(state.pool)}${hint ? ` · ${hint}` : ''}`;
  };
  watch(line, (added) => {
    render();
    if (added > 0 && !reducedMotion.matches) {
      const float = make('span', 'jackpot-float', `+${added} в джекпот`);
      float.addEventListener('animationend', () => float.remove());
      line.append(float);
    }
  });
  input.addEventListener('input', render);
  render();
  return line;
}

function confetti(): HTMLElement {
  const box = make('div', 'confetti');
  if (reducedMotion.matches) {
    return box;
  }
  const colors = ['#fde68a', '#fbbf24', '#f59e0b', '#a855f7', '#22c55e', '#38bdf8', '#f472b6'];
  for (let i = 0; i < confettiCount; i++) {
    const piece = make('i', '');
    piece.style.left = `${Math.random() * 100}%`;
    piece.style.background = colors[i % colors.length];
    piece.style.animationDelay = `${Math.random() * 0.6}s`;
    piece.style.animationDuration = `${1.8 + Math.random() * 1.4}s`;
    piece.style.setProperty('--drift', `${(Math.random() * 2 - 1) * 120}px`);
    piece.style.setProperty('--spin', `${Math.random() * 720 - 360}deg`);
    box.append(piece);
  }
  return box;
}

/** Праздник срыва джекпота. Картинка для него будет позже — пока только текст. */
export function showJackpotWin(amount: number): void {
  const title = make('h2', 'jackpot-win-title', '💎 ДЖЕКПОТ!');
  const sum = make('p', 'jackpot-win-sum', `+${points(amount)}`);
  const close = make('button', 'primary overlay-close', 'Забрать! 🤑');
  showOverlay('jackpot-win', `Джекпот: ${points(amount)}`, [title, sum, close], close, confetti());
}
