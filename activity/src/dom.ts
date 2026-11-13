import { jackpotLine } from './jackpot';

export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className = '',
  text = '',
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text) {
    node.textContent = text;
  }
  return node;
}

export const stakeMin = 10;
export const stakeMax = 1000;

export interface StakeField {
  /** Поле ставки и под ним строка джекпота с шансом сорвать его этой ставкой. */
  row: HTMLDivElement;
  read: () => number | null;
}

export function stakeField(initial = 20): StakeField {
  const input = el('input', 'stake');
  input.type = 'number';
  input.min = String(stakeMin);
  input.max = String(stakeMax);
  input.value = String(initial);
  const label = el('label', 'stake-row', 'Ставка, поинтов');
  label.append(input);
  const read = (): number | null => {
    const n = Number(input.value);
    return Number.isInteger(n) && n >= stakeMin && n <= stakeMax ? n : null;
  };
  const row = el('div', 'stake-field');
  row.append(label, jackpotLine(input, read));
  return { row, read };
}

export const stakeError = `Ставка — от ${stakeMin} до ${stakeMax} поинтов`;

export const errorText = (e: unknown): string => (e instanceof Error ? e.message : 'Что-то пошло не так');

export function hiDpiCanvas(className: string, width: number, height: number): CanvasRenderingContext2D | null {
  const canvas = el('canvas', className);
  const dpr = (window.devicePixelRatio || 1) * 2;
  canvas.width = width * dpr;
  canvas.height = height * dpr;
  const ctx = canvas.getContext('2d');
  ctx?.scale(dpr, dpr);
  return ctx;
}

export type Cleanup = () => void;

export type Screen = (root: HTMLElement) => Cleanup;
