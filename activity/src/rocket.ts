import { request, type RocketPoll, type RocketResult, type RocketStart } from './api';
import { el, errorText, hiDpiCanvas, stakeError, stakeField, type Cleanup } from './dom';

type Phase = 'idle' | 'flying' | 'won' | 'crashed';

const width = 360;
const height = 300;
const pad = 28;
const samples = 80;
const autoMin = 1.01;
const autoMax = 100;
const colors = {
  axis: '#3f4147',
  line: '#facc15',
  won: '#22c55e',
  crashed: '#dc2626',
  text: '#f2f3f5',
  muted: '#b5bac1',
};

const multiplierAt = (growth: number, seconds: number): number => Math.floor(Math.exp(growth * seconds) * 100) / 100;

const secondsTo = (growth: number, m: number): number => Math.log(m) / growth;

function draw(ctx: CanvasRenderingContext2D, growth: number, seconds: number, phase: Phase, shown: number): void {
  ctx.clearRect(0, 0, width, height);
  const xMax = Math.max(8, seconds * 1.15);
  const yMax = Math.max(2, Math.exp(growth * seconds) * 1.15);
  const x = (s: number): number => pad + (s / xMax) * (width - 2 * pad);
  const y = (m: number): number => height - pad - ((m - 1) / (yMax - 1)) * (height - 2 * pad);

  ctx.strokeStyle = colors.axis;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(pad, pad);
  ctx.lineTo(pad, height - pad);
  ctx.lineTo(width - pad, height - pad);
  ctx.stroke();

  const accent = phase === 'crashed' ? colors.crashed : phase === 'won' ? colors.won : colors.line;
  ctx.strokeStyle = accent;
  ctx.lineWidth = 3;
  ctx.beginPath();
  for (let i = 0; i <= samples; i++) {
    const s = (seconds * i) / samples;
    const px = x(s);
    const py = y(Math.exp(growth * s));
    if (i === 0) {
      ctx.moveTo(px, py);
    } else {
      ctx.lineTo(px, py);
    }
  }
  ctx.stroke();

  if (phase !== 'idle') {
    ctx.font = '28px system-ui, sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(phase === 'crashed' ? '💥' : '🚀', x(seconds), y(Math.exp(growth * seconds)));
  }

  ctx.fillStyle = phase === 'idle' ? colors.muted : accent;
  ctx.font = 'bold 44px system-ui, sans-serif';
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.fillText(`x${shown.toFixed(2)}`, width / 2, height / 2 - 30);
  ctx.textBaseline = 'alphabetic';
}

function verdict(res: RocketResult): string {
  return res.status === 'won'
    ? `🤑 Забрал на x${res.multiplier.toFixed(2)}: выигрыш ${res.payout} фантиков. Ракета взорвалась бы на x${res.crash.toFixed(2)}`
    : `💥 Взрыв на x${res.crash.toFixed(2)}`;
}

export function mountRocket(root: HTMLElement): Cleanup {
  const ctx = hiDpiCanvas('chart', width, height);
  if (!ctx) {
    root.append(el('p', 'status error', 'Браузер не умеет рисовать график'));
    return () => undefined;
  }

  let disposed = false;
  let frame = 0;
  let round: RocketStart | null = null;
  let startedAt = 0;
  let settled = false;

  const stake = stakeField();
  const auto = el('input', 'stake');
  auto.type = 'number';
  auto.min = String(autoMin);
  auto.max = String(autoMax);
  auto.step = '0.01';
  auto.placeholder = 'нет';
  const autoRow = el('label', 'stake-row', 'Автовывод, x');
  autoRow.append(auto);
  const button = el('button', 'primary', 'Запуск');
  const status = el('p', 'status', 'Забери до взрыва');

  const readAuto = (): number | null => {
    if (auto.value.trim() === '') {
      return 0;
    }
    const n = Number(auto.value);
    return Number.isFinite(n) && n >= autoMin && n <= autoMax ? n : null;
  };

  const finish = (id: string, res: RocketResult): void => {
    if (disposed || settled || !round || round.id !== id) {
      return;
    }
    settled = true;
    cancelAnimationFrame(frame);
    const end = res.status === 'won' ? res.multiplier : res.crash;
    draw(ctx, round.growth, secondsTo(round.growth, end), res.status, end);
    status.textContent = verdict(res);
    console.log({ rocket: res });
    button.textContent = 'Запуск';
    button.disabled = false;
    auto.disabled = false;
    round = null;
  };

  const fail = (id: string | null, e: unknown): void => {
    if (disposed || (id !== null && round?.id !== id)) {
      return;
    }
    cancelAnimationFrame(frame);
    status.textContent = errorText(e);
    button.textContent = 'Запуск';
    button.disabled = false;
    auto.disabled = false;
    round = null;
  };

  const fly = (): void => {
    if (!round || settled) {
      return;
    }
    const seconds = (performance.now() - startedAt) / 1000;
    const m = Math.min(round.maxMultiplier, multiplierAt(round.growth, seconds));
    draw(ctx, round.growth, seconds, 'flying', m);
    frame = requestAnimationFrame(fly);
  };

  const watch = async (id: string): Promise<void> => {
    while (!disposed && round?.id === id) {
      const res = await request<RocketPoll>('GET', `/rocket/${id}`);
      if (res.status !== 'flying') {
        finish(id, res);
        return;
      }
    }
  };

  const launch = async (): Promise<void> => {
    const amount = stake.read();
    if (amount === null) {
      status.textContent = stakeError;
      return;
    }
    const autoValue = readAuto();
    if (autoValue === null) {
      status.textContent = `Автовывод — от x${autoMin} до x${autoMax} или пусто`;
      return;
    }
    button.disabled = true;
    auto.disabled = true;
    try {
      const started = await request<RocketStart>('POST', '/rocket', { stake: amount, auto: autoValue });
      if (disposed) {
        return;
      }
      round = started;
      settled = false;
      startedAt = performance.now();
      button.textContent = 'Забрать';
      button.disabled = false;
      status.textContent = autoValue ? `Летим, автовывод на x${autoValue.toFixed(2)}` : 'Летим!';
      frame = requestAnimationFrame(fly);
      watch(started.id).catch((e: unknown) => fail(started.id, e));
    } catch (e) {
      fail(null, e);
    }
  };

  const cashout = async (): Promise<void> => {
    if (!round) {
      return;
    }
    const { id } = round;
    button.disabled = true;
    try {
      finish(id, await request<RocketResult>('POST', '/rocket/cashout', { id }));
    } catch (e) {
      fail(id, e);
    }
  };

  button.addEventListener('click', () => {
    void (round ? cashout() : launch());
  });

  const controls = el('div', 'controls');
  controls.append(stake.row, autoRow, button, status);
  const layout = el('section', 'game rocket');
  layout.append(ctx.canvas, controls);
  root.append(layout);
  draw(ctx, 0.06, 0, 'idle', 1);

  return () => {
    disposed = true;
    cancelAnimationFrame(frame);
  };
}
