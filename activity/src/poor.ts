import { el } from './dom';
import { showOverlay } from './overlay';

const caption = 'Ты слишком беден для этого действия';

/** Картинка «ты слишком беден» поверх игры. */
export function showTooPoor(): void {
  const img = el('img', 'poor-img');
  img.src = 'too-poor.webp';
  img.alt = caption;
  const close = el('button', 'primary overlay-close', 'Пойду отжиматься 💪');
  showOverlay('poor', caption, [img, close], close);
}
