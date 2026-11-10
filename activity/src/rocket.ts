import { points, refreshBalance } from './wallet';
import { request, type RocketPoll, type RocketResult, type RocketStart } from './api';
import { el, errorText, hiDpiCanvas, stakeError, stakeField, type Cleanup } from './dom';

type Phase = 'idle' | 'flying' | 'crashed';

const width = 360;
const height = 300;
const pad = 28;
const samples = 80;
const autoMin = 1.01;
const autoMax = 99.99;
const tailMaxSeconds = 4;
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

const rocketBase = 28;
const rocketGrowth = 0.45;
const rocketScaleMax = 2.4;
const shakeFrom = 1.3;
const shakePerLog = 2.5;
const shakeMax = 6;
const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)');

/** Чем выше множитель, тем ракета крупнее и сильнее трясётся. */
const rocketScale = (m: number): number => Math.min(rocketScaleMax, 1 + rocketGrowth * Math.log(m));

const shakeAmp = (m: number): number =>
  reducedMotion.matches ? 0 : Math.min(shakeMax, Math.max(0, Math.log(m / shakeFrom)) * shakePerLog);

const jitter = (amp: number): number => (Math.random() * 2 - 1) * amp;

function drawRocket(ctx: CanvasRenderingContext2D, px: number, py: number, heading: number, m: number, phase: Phase): void {
  const size = rocketBase * rocketScale(m);
  const half = size / 2;
  const amp = phase === 'flying' ? shakeAmp(m) : 0;
  ctx.save();
  ctx.translate(
    Math.min(width - half, Math.max(half, px)) + jitter(amp),
    Math.min(height - half, Math.max(half, py)) + jitter(amp),
  );
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  if (phase === 'crashed') {
    ctx.font = `${size * 1.2}px system-ui, sans-serif`;
    ctx.fillText('💥', 0, 0);
    ctx.restore();
    return;
  }
  ctx.rotate(heading + jitter(amp * 0.02));
  if (phase === 'flying') {
    // пламя за хвостом, мерцает и растёт вместе с ракетой
    const r = half * (0.55 + Math.random() * 0.25);
    const fx = -half * 0.75;
    const flame = ctx.createRadialGradient(fx, 0, 0, fx, 0, r);
    flame.addColorStop(0, 'rgb(255 244 200 / 95%)');
    flame.addColorStop(0.4, 'rgb(251 146 60 / 75%)');
    flame.addColorStop(1, 'rgb(220 38 38 / 0%)');
    ctx.fillStyle = flame;
    ctx.beginPath();
    ctx.arc(fx, 0, r, 0, Math.PI * 2);
    ctx.fill();
  }
  // эмодзи 🚀 нарисовано носом вправо-вверх, разворачиваем его по курсу
  ctx.rotate(Math.PI / 4);
  ctx.font = `${size}px system-ui, sans-serif`;
  ctx.fillText('🚀', 0, 0);
  ctx.restore();
}

interface Scale {
  x: (s: number) => number;
  y: (m: number) => number;
}

function curve(ctx: CanvasRenderingContext2D, scale: Scale, growth: number, from: number, to: number, color: string): void {
  ctx.strokeStyle = color;
  ctx.lineWidth = 3;
  ctx.beginPath();
  for (let i = 0; i <= samples; i++) {
    const s = from + ((to - from) * i) / samples;
    const px = scale.x(s);
    const py = scale.y(Math.exp(growth * s));
    if (i === 0) {
      ctx.moveTo(px, py);
    } else {
      ctx.lineTo(px, py);
    }
  }
  ctx.stroke();
}

function cashMark(ctx: CanvasRenderingContext2D, px: number, py: number, cashedAt: number): void {
  ctx.fillStyle = colors.won;
  ctx.beginPath();
  ctx.arc(px, py, 6, 0, Math.PI * 2);
  ctx.fill();
  const label = `x${cashedAt.toFixed(2)}`;
  ctx.font = 'bold 15px system-ui, sans-serif';
  ctx.textAlign = 'left';
  ctx.textBaseline = 'top';
  ctx.fillText(label, Math.min(px + 8, width - pad - ctx.measureText(label).width), Math.min(py + 8, height - pad - 18));
}

function draw(
  ctx: CanvasRenderingContext2D,
  growth: number,
  seconds: number,
  phase: Phase,
  shown: number,
  cashedAt = 0,
): void {
  ctx.clearRect(0, 0, width, height);
  const xMax = Math.max(8, seconds * 1.15);
  const yMax = Math.max(2, Math.exp(growth * seconds) * 1.15);
  const scale: Scale = {
    x: (s) => pad + (s / xMax) * (width - 2 * pad),
    y: (m) => height - pad - ((m - 1) / (yMax - 1)) * (height - 2 * pad),
  };
  const { x, y } = scale;

  ctx.strokeStyle = colors.axis;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(pad, pad);
  ctx.lineTo(pad, height - pad);
  ctx.lineTo(width - pad, height - pad);
  ctx.stroke();

  const accent = phase === 'crashed' ? colors.crashed : colors.line;
  const cashSeconds = cashedAt > 0 ? Math.min(seconds, secondsTo(growth, cashedAt)) : 0;
  curve(ctx, scale, growth, 0, cashSeconds, colors.won);
  curve(ctx, scale, growth, cashSeconds, seconds, accent);
  if (cashedAt > 0) {
    cashMark(ctx, x(cashSeconds), y(cashedAt), cashedAt);
  }

  if (phase !== 'idle') {
    const m = Math.exp(growth * seconds);
    // направление полёта — касательная к кривой в экранных координатах
    const dx = (width - 2 * pad) / xMax;
    const dy = (-growth * m * (height - 2 * pad)) / (yMax - 1);
    drawRocket(ctx, x(seconds), y(m), Math.atan2(dy, dx), m, phase);
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
    ? `🤑 Забрал на x${res.multiplier.toFixed(2)}: выигрыш ${points(res.payout)}. Ракета взорвалась бы на x${res.crash.toFixed(2)}`
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

  const flyOn = (growth: number, cashedAt: number, crash: number): void => {
    const from = secondsTo(growth, cashedAt);
    const end = secondsTo(growth, crash);
    const speed = Math.max(1, (end - from) / tailMaxSeconds);
    const t0 = performance.now();
    const step = (now: number): void => {
      const seconds = Math.min(end, from + (Math.max(0, now - t0) / 1000) * speed);
      if (seconds >= end) {
        draw(ctx, growth, end, 'crashed', crash, cashedAt);
        return;
      }
      draw(ctx, growth, seconds, 'flying', multiplierAt(growth, seconds), cashedAt);
      frame = requestAnimationFrame(step);
    };
    frame = requestAnimationFrame(step);
  };

  const finish = (id: string, res: RocketResult): void => {
    if (disposed || settled || !round || round.id !== id) {
      return;
    }
    settled = true;
    cancelAnimationFrame(frame);
    if (res.status === 'won') {
      flyOn(round.growth, res.multiplier, res.crash);
    } else {
      draw(ctx, round.growth, secondsTo(round.growth, res.crash), 'crashed', res.crash);
    }
    status.textContent = verdict(res);
    console.log({ rocket: res });
    void refreshBalance();
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
      void refreshBalance();
      startedAt = performance.now();
      button.textContent = 'Забрать';
      button.disabled = false;
      status.textContent = autoValue ? `Летим, автовывод на x${autoValue.toFixed(2)}` : 'Летим!';
      cancelAnimationFrame(frame);
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
