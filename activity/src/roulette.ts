import { points, refreshBalance, spend } from './wallet';
import { request, type RouletteBet, type RouletteResult } from './api';
import { el, errorText, hiDpiCanvas, stakeError, stakeField, type Cleanup } from './dom';

interface BetOption {
  label: string;
  bet: RouletteBet;
  className: string;
}

const wheelOrder = [
  0, 32, 15, 19, 4, 21, 2, 25, 17, 34, 6, 27, 13, 36, 11, 30, 8, 23, 10, 5, 24, 16, 33, 1, 20, 14, 31, 9, 22, 18, 29,
  7, 28, 12, 35, 3, 26,
];
const reds = new Set([1, 3, 5, 7, 9, 12, 14, 16, 18, 19, 21, 23, 25, 27, 30, 32, 34, 36]);
const turn = Math.PI * 2;
const seg = turn / wheelOrder.length;
const size = 300;
const spinMs = 4500;
const minTurns = 5;
const colors = {
  red: '#dc2626',
  black: '#18181b',
  green: '#16a34a',
  rim: '#111214',
  hub: '#232428',
  text: '#ffffff',
  gold: '#facc15',
};

const outsideBets: BetOption[] = [
  { label: 'Красное', bet: { kind: 'red' }, className: 'bet red' },
  { label: 'Чёрное', bet: { kind: 'black' }, className: 'bet black' },
  { label: 'Чёт', bet: { kind: 'even' }, className: 'bet' },
  { label: 'Нечет', bet: { kind: 'odd' }, className: 'bet' },
  { label: '1–18', bet: { kind: 'low' }, className: 'bet' },
  { label: '19–36', bet: { kind: 'high' }, className: 'bet' },
  { label: '1–12', bet: { kind: 'dozen', value: 1 }, className: 'bet' },
  { label: '13–24', bet: { kind: 'dozen', value: 2 }, className: 'bet' },
  { label: '25–36', bet: { kind: 'dozen', value: 3 }, className: 'bet' },
];

const numberColor = (n: number): string => (n === 0 ? colors.green : reds.has(n) ? colors.red : colors.black);

const colorClass = (n: number): string => (n === 0 ? 'green' : reds.has(n) ? 'red' : 'black');

const easeOut = (t: number): number => 1 - (1 - t) ** 4;

const numberBets: BetOption[] = Array.from({ length: 37 }, (_, n) => ({
  label: String(n),
  bet: { kind: 'number', value: n },
  className: `bet num ${colorClass(n)}`,
}));

function targetAngle(current: number, pocket: number): number {
  const base = -pocket * seg;
  const k = Math.ceil((current + minTurns * turn - base) / turn);
  return base + k * turn;
}

function drawWheel(ctx: CanvasRenderingContext2D, angle: number, shown: number | null): void {
  const c = size / 2;
  const r = c - 10;
  ctx.clearRect(0, 0, size, size);
  wheelOrder.forEach((n, i) => {
    const a0 = angle + i * seg - seg / 2 - Math.PI / 2;
    ctx.beginPath();
    ctx.moveTo(c, c);
    ctx.arc(c, c, r, a0, a0 + seg);
    ctx.closePath();
    ctx.fillStyle = numberColor(n);
    ctx.fill();
    ctx.strokeStyle = colors.rim;
    ctx.lineWidth = 1;
    ctx.stroke();
    ctx.save();
    ctx.translate(c, c);
    ctx.rotate(a0 + seg / 2 + Math.PI / 2);
    ctx.fillStyle = colors.text;
    ctx.font = 'bold 11px system-ui, sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText(String(n), 0, -r + 16);
    ctx.restore();
  });
  ctx.beginPath();
  ctx.arc(c, c, r * 0.58, 0, turn);
  ctx.fillStyle = colors.hub;
  ctx.fill();
  ctx.beginPath();
  ctx.arc(c, c, r, 0, turn);
  ctx.strokeStyle = colors.rim;
  ctx.lineWidth = 6;
  ctx.stroke();
  if (shown !== null) {
    ctx.fillStyle = numberColor(shown) === colors.black ? colors.text : numberColor(shown);
    ctx.font = 'bold 48px system-ui, sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(String(shown), c, c);
    ctx.textBaseline = 'alphabetic';
  }
  ctx.beginPath();
  ctx.moveTo(c - 11, 0);
  ctx.lineTo(c + 11, 0);
  ctx.lineTo(c, 24);
  ctx.closePath();
  ctx.fillStyle = colors.gold;
  ctx.fill();
}

function verdict(res: RouletteResult): string {
  const head = `Выпало ${res.number}.`;
  return res.payout > 0 ? `${head} 🤑 Выигрыш ${points(res.payout)} (x${res.multiplier})` : `${head} 💀 Мимо`;
}

export function mountRoulette(root: HTMLElement): Cleanup {
  const ctx = hiDpiCanvas('wheel', size, size);
  if (!ctx) {
    root.append(el('p', 'status error', 'Браузер не умеет рисовать колесо'));
    return () => undefined;
  }

  let angle = 0;
  let frame = 0;
  let disposed = false;
  let selected = outsideBets[0];
  const betButtons: HTMLButtonElement[] = [];

  const stake = stakeField();
  const spin = el('button', 'primary', 'Крутить');
  const status = el('p', 'status', 'Выбери ставку и крути');

  const choose = (option: BetOption, button: HTMLButtonElement): void => {
    selected = option;
    for (const b of betButtons) {
      b.classList.toggle('selected', b === button);
    }
  };

  const betGrid = (options: BetOption[], className: string): HTMLDivElement => {
    const grid = el('div', className);
    for (const option of options) {
      const button = el('button', option.className, option.label);
      button.addEventListener('click', () => choose(option, button));
      betButtons.push(button);
      grid.append(button);
    }
    return grid;
  };

  const animate = (target: number, done: () => void): void => {
    const from = angle;
    const t0 = performance.now();
    const step = (now: number): void => {
      const t = Math.min(1, (now - t0) / spinMs);
      angle = from + (target - from) * easeOut(t);
      drawWheel(ctx, angle, null);
      if (t < 1) {
        frame = requestAnimationFrame(step);
      } else {
        done();
      }
    };
    frame = requestAnimationFrame(step);
  };

  const play = async (): Promise<void> => {
    const amount = stake.read();
    if (amount === null) {
      status.textContent = stakeError;
      return;
    }
    spin.disabled = true;
    status.textContent = 'Ставки сделаны…';
    try {
      const res = await request<RouletteResult>('POST', '/roulette', {
        id: crypto.randomUUID(),
        stake: amount,
        bet: selected.bet,
      });
      if (disposed) {
        return;
      }
      console.log({ roulette: res });
      spend(amount);
      animate(targetAngle(angle, wheelOrder.indexOf(res.number)), () => {
        drawWheel(ctx, angle, res.number);
        status.textContent = verdict(res);
        spin.disabled = false;
        void refreshBalance();
      });
    } catch (e) {
      if (!disposed) {
        status.textContent = errorText(e);
        spin.disabled = false;
      }
    }
  };

  spin.addEventListener('click', () => {
    void play();
  });

  const controls = el('div', 'controls');
  controls.append(stake.row, betGrid(outsideBets, 'bets'), betGrid(numberBets, 'numbers'), spin, status);
  const layout = el('section', 'game roulette');
  layout.append(ctx.canvas, controls);
  root.append(layout);
  choose(outsideBets[0], betButtons[0]);
  drawWheel(ctx, angle, null);

  return () => {
    disposed = true;
    cancelAnimationFrame(frame);
  };
}
