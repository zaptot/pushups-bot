import { points, refreshBalance, spend } from './wallet';
import { request, type PlinkoResult, type PlinkoRisk, type PlinkoTables } from './api';
import { el, errorText, hiDpiCanvas, stakeError, stakeField, type Cleanup } from './dom';

interface Point {
  x: number;
  y: number;
}

interface Geometry {
  n: number;
  cx: number;
  gap: number;
  rowH: number;
  pegR: number;
  ballR: number;
}

const width = 360;
const height = 380;
const top = 22;
const slotH = 26;
const pad = 10;
const hopMs = 140;
const rowsMin = 8;
const rowsMax = 16;
const colors = { peg: '#e5e7eb', ball: '#facc15', text: '#18181b', dim: '#3f4147' };
const risks: { id: PlinkoRisk; label: string }[] = [
  { id: 'low', label: 'Низкий' },
  { id: 'medium', label: 'Средний' },
  { id: 'high', label: 'Высокий' },
];

const geometry = (n: number): Geometry => {
  const gap = (width - 2 * pad) / (n + 2);
  return {
    n,
    cx: width / 2,
    gap,
    rowH: (height - 4 - top - slotH) / (n - 0.4),
    pegR: Math.max(2, gap * 0.09),
    ballR: Math.max(4, gap * 0.22),
  };
};

const pegY = (g: Geometry, row: number): number => top + row * g.rowH;

const slotTop = (g: Geometry): number => pegY(g, g.n - 1) + g.rowH * 0.6;

const slotX = (g: Geometry, k: number): number => g.cx + (k - g.n / 2) * g.gap;

const slotColor = (k: number, n: number): string => `hsl(${50 - (50 * Math.abs(k - n / 2)) / (n / 2)} 85% 52%)`;

const label = (m: number): string => (m >= 10 ? String(Math.round(m)) : m < 0.1 ? String(m) : String(+m.toFixed(1)));

function pathPoints(g: Geometry, path: number[]): Point[] {
  const points: Point[] = [{ x: g.cx, y: 0 }];
  let pos = 0;
  for (let row = 0; row < g.n; row++) {
    points.push({ x: g.cx + (pos - row / 2) * g.gap, y: pegY(g, row) - g.pegR - g.ballR });
    pos += path[row];
  }
  points.push({ x: slotX(g, pos), y: slotTop(g) + slotH / 2 });
  return points;
}

interface Ball {
  points: Point[];
  t0: number;
  res: PlinkoResult;
}

interface Series {
  count: number;
  dropped: number;
  stake: number;
  payout: number;
  last: PlinkoResult | null;
  failure: string;
}

const ballCounts = [1, 3, 5, 10, 20];
const betGapMs = 1100;
const bounceMs = 300;

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

function ballAt(g: Geometry, ball: Ball, now: number): Point | null {
  const elapsed = Math.max(0, (now - ball.t0) / hopMs);
  const { points } = ball;
  if (elapsed >= points.length - 1) {
    return null;
  }
  const i = Math.floor(elapsed);
  const u = elapsed - i;
  const a = points[i];
  const b = points[i + 1];
  const lift = i === 0 ? 0 : g.rowH * 0.45 * 4 * u * (1 - u);
  return { x: a.x + (b.x - a.x) * u, y: a.y + (b.y - a.y) * u - lift };
}

function drawBoard(
  ctx: CanvasRenderingContext2D,
  g: Geometry,
  table: number[],
  lit: ReadonlySet<number>,
  dim: boolean,
  balls: Point[],
): void {
  ctx.clearRect(0, 0, width, height);
  ctx.fillStyle = colors.peg;
  for (let row = 0; row < g.n; row++) {
    for (let m = 0; m < row + 3; m++) {
      ctx.beginPath();
      ctx.arc(g.cx + (m - 1 - row / 2) * g.gap, pegY(g, row), g.pegR, 0, Math.PI * 2);
      ctx.fill();
    }
  }
  const y = slotTop(g);
  ctx.font = `bold ${Math.max(8, Math.min(12, g.gap * 0.42))}px system-ui, sans-serif`;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  table.forEach((m, k) => {
    const x = slotX(g, k) - g.gap / 2 + 1;
    const sy = lit.has(k) ? y + 4 : y;
    ctx.fillStyle = dim && !lit.has(k) ? colors.dim : slotColor(k, g.n);
    ctx.fillRect(x, sy, g.gap - 2, slotH);
    ctx.fillStyle = colors.text;
    ctx.fillText(label(m), slotX(g, k), sy + slotH / 2);
  });
  ctx.textBaseline = 'alphabetic';
  ctx.fillStyle = colors.ball;
  for (const ball of balls) {
    ctx.beginPath();
    ctx.arc(ball.x, ball.y, g.ballR, 0, Math.PI * 2);
    ctx.fill();
  }
}

function verdict(res: PlinkoResult): string {
  const mood = res.multiplier >= 1 ? '🤑' : '💀';
  return `${mood} x${res.multiplier}: выигрыш ${points(res.payout)}`;
}

function summary(s: Series): string {
  if (s.dropped === 1 && s.last) {
    return verdict(s.last);
  }
  const mood = s.payout >= s.stake ? '🤑' : '💀';
  return `${mood} Шариков: ${s.dropped}, ставки ${points(s.stake)}, выигрыш ${points(s.payout)}`;
}

export function mountPlinko(root: HTMLElement): Cleanup {
  const ctx = hiDpiCanvas('board', width, height);
  if (!ctx) {
    root.append(el('p', 'status error', 'Браузер не умеет рисовать доску'));
    return () => undefined;
  }

  let tables: PlinkoTables | null = null;
  let rows = 12;
  let risk: PlinkoRisk = 'low';
  let frame = 0;
  let busy = false;
  let disposed = false;
  let running = false;
  let stopped = false;
  let lastSent = 0;
  let balls: Ball[] = [];
  const landedAt = new Map<number, number>();
  let series: Series = { count: 0, dropped: 0, stake: 0, payout: 0, last: null, failure: '' };

  const stake = stakeField();
  const countSelect = el('select', 'select');
  for (const n of ballCounts) {
    const option = el('option', '', String(n));
    option.value = String(n);
    countSelect.append(option);
  }
  const countRow = el('label', 'stake-row', 'Шариков');
  countRow.append(countSelect);
  const dropLabel = 'Бросить';
  const drop = el('button', 'primary', dropLabel);
  drop.disabled = true;
  const status = el('p', 'status', 'Загрузка таблиц…');

  const table = (): number[] => tables?.[String(rows)]?.[risk] ?? [];

  const redraw = (): void => {
    drawBoard(ctx, geometry(rows), table(), new Set(), false, []);
  };

  const rowsSelect = el('select', 'select');
  for (let n = rowsMin; n <= rowsMax; n++) {
    const option = el('option', '', String(n));
    option.value = String(n);
    option.selected = n === rows;
    rowsSelect.append(option);
  }
  rowsSelect.addEventListener('change', () => {
    rows = Number(rowsSelect.value);
    if (!busy) {
      redraw();
    }
  });
  const rowsRow = el('label', 'stake-row', 'Ряды');
  rowsRow.append(rowsSelect);

  const riskRow = el('div', 'bets');
  const riskButtons = risks.map(({ id, label: text }) => {
    const button = el('button', 'bet', text);
    button.addEventListener('click', () => {
      risk = id;
      for (const b of riskButtons) {
        b.classList.toggle('selected', b === button);
      }
      if (!busy) {
        redraw();
      }
    });
    riskRow.append(button);
    return button;
  });
  riskButtons[0].classList.add('selected');

  const setBusy = (value: boolean): void => {
    busy = value;
    drop.disabled = value;
    rowsSelect.disabled = value;
    countSelect.disabled = value;
    for (const b of riskButtons) {
      b.disabled = value;
    }
  };

  const progress = (): void => {
    status.textContent =
      series.count === 1 ? 'Шарик летит…' : `Шарик ${series.dropped} из ${series.count}, выигрыш ${points(series.payout)}`;
  };

  const finishSeries = (): void => {
    drawBoard(ctx, geometry(rows), table(), new Set(landedAt.keys()), true, []);
    status.textContent = series.failure || summary(series);
    void refreshBalance();
    setBusy(false);
  };

  const render = (now: number): void => {
    const g = geometry(rows);
    const flying: Point[] = [];
    balls = balls.filter((ball) => {
      const at = ballAt(g, ball, now);
      if (at) {
        flying.push(at);
        return true;
      }
      landedAt.set(ball.res.slot, now);
      series.payout += ball.res.payout;
      if (running) {
        progress();
      }
      return false;
    });
    if (balls.length === 0 && !running) {
      finishSeries();
      return;
    }
    const lit = new Set([...landedAt].filter(([, t]) => now - t < bounceMs).map(([slot]) => slot));
    drawBoard(ctx, g, table(), lit, false, flying);
    frame = requestAnimationFrame(render);
  };

  const launchBall = (res: PlinkoResult): void => {
    balls.push({ points: pathPoints(geometry(res.rows), res.path), t0: performance.now(), res });
    if (balls.length === 1) {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(render);
    }
  };

  const play = async (): Promise<void> => {
    const amount = stake.read();
    if (amount === null) {
      status.textContent = stakeError;
      return;
    }
    const count = Number(countSelect.value);
    setBusy(true);
    running = true;
    stopped = false;
    landedAt.clear();
    series = { count, dropped: 0, stake: 0, payout: 0, last: null, failure: '' };
    status.textContent = 'Шарик летит…';
    if (count > 1) {
      drop.textContent = 'Стоп';
      drop.disabled = false;
    }
    try {
      for (let i = 0; i < count && !stopped; i++) {
        await sleep(Math.max(0, lastSent + betGapMs - performance.now()));
        if (stopped || disposed) {
          break;
        }
        lastSent = performance.now();
        const res = await request<PlinkoResult>('POST', '/plinko', { id: crypto.randomUUID(), stake: amount, rows, risk });
        if (disposed) {
          return;
        }
        console.log({ plinko: res });
        spend(amount);
        series = { ...series, dropped: series.dropped + 1, stake: series.stake + amount, last: res };
        progress();
        launchBall(res);
      }
    } catch (e) {
      series.failure = errorText(e);
    }
    if (disposed) {
      return;
    }
    running = false;
    drop.textContent = dropLabel;
    drop.disabled = true;
    if (balls.length === 0) {
      finishSeries();
    }
  };

  drop.addEventListener('click', () => {
    if (running) {
      stopped = true;
      drop.disabled = true;
      return;
    }
    void play();
  });

  const controls = el('div', 'controls');
  controls.append(stake.row, countRow, rowsRow, el('span', 'hint', 'Риск'), riskRow, drop, status);
  const layout = el('section', 'game plinko');
  layout.append(ctx.canvas, controls);
  root.append(layout);
  redraw();

  request<PlinkoTables>('GET', '/plinko/tables')
    .then((loaded) => {
      if (disposed) {
        return;
      }
      tables = loaded;
      redraw();
      drop.disabled = false;
      status.textContent = 'Выбери ряды и риск';
    })
    .catch((e: unknown) => {
      if (!disposed) {
        status.textContent = errorText(e);
      }
    });

  return () => {
    disposed = true;
    cancelAnimationFrame(frame);
  };
}
