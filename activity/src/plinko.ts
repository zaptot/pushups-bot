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

function drawBoard(ctx: CanvasRenderingContext2D, g: Geometry, table: number[], lit: number | null, ball: Point | null): void {
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
    ctx.fillStyle = lit === null || lit === k ? slotColor(k, g.n) : colors.dim;
    ctx.fillRect(x, lit === k ? y + 4 : y, g.gap - 2, slotH);
    ctx.fillStyle = colors.text;
    ctx.fillText(label(m), slotX(g, k), (lit === k ? y + 4 : y) + slotH / 2);
  });
  ctx.textBaseline = 'alphabetic';
  if (ball) {
    ctx.beginPath();
    ctx.arc(ball.x, ball.y, g.ballR, 0, Math.PI * 2);
    ctx.fillStyle = colors.ball;
    ctx.fill();
  }
}

function verdict(res: PlinkoResult): string {
  const mood = res.multiplier >= 1 ? '🤑' : '💀';
  return `${mood} x${res.multiplier}: засчитано ${res.payout}. Осталось: ${res.remaining}`;
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

  const stake = stakeField();
  const drop = el('button', 'primary', 'Бросить шарик');
  drop.disabled = true;
  const status = el('p', 'status', 'Загрузка таблиц…');

  const table = (): number[] => tables?.[String(rows)]?.[risk] ?? [];

  const redraw = (): void => {
    drawBoard(ctx, geometry(rows), table(), null, null);
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
    for (const b of riskButtons) {
      b.disabled = value;
    }
  };

  const animate = (res: PlinkoResult): void => {
    const g = geometry(res.rows);
    const points = pathPoints(g, res.path);
    const hop = g.rowH * 0.45;
    const t0 = performance.now();
    const step = (now: number): void => {
      const elapsed = Math.max(0, (now - t0) / hopMs);
      const i = Math.min(points.length - 2, Math.floor(elapsed));
      const u = Math.min(1, elapsed - i);
      const a = points[i];
      const b = points[i + 1];
      const lift = i === 0 ? 0 : hop * 4 * u * (1 - u);
      drawBoard(ctx, g, tables?.[String(res.rows)]?.[res.risk] ?? [], null, {
        x: a.x + (b.x - a.x) * u,
        y: a.y + (b.y - a.y) * u - lift,
      });
      if (elapsed < points.length - 1) {
        frame = requestAnimationFrame(step);
        return;
      }
      drawBoard(ctx, g, tables?.[String(res.rows)]?.[res.risk] ?? [], res.slot, null);
      status.textContent = verdict(res);
      setBusy(false);
    };
    frame = requestAnimationFrame(step);
  };

  const play = async (): Promise<void> => {
    const amount = stake.read();
    if (amount === null) {
      status.textContent = stakeError;
      return;
    }
    setBusy(true);
    status.textContent = 'Шарик летит…';
    try {
      const res = await request<PlinkoResult>('POST', '/plinko', { id: crypto.randomUUID(), stake: amount, rows, risk });
      if (disposed) {
        return;
      }
      console.log({ plinko: res });
      animate(res);
    } catch (e) {
      if (!disposed) {
        status.textContent = errorText(e);
        setBusy(false);
      }
    }
  };

  drop.addEventListener('click', () => {
    void play();
  });

  const controls = el('div', 'controls');
  controls.append(stake.row, rowsRow, el('span', 'hint', 'Риск'), riskRow, drop, status);
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
