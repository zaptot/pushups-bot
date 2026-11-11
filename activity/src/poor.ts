import { el } from './dom';

const caption = 'Ты слишком беден для этого действия';

let open: HTMLDivElement | null = null;

/** Картинка «ты слишком беден» поверх игры; закрывается кликом, кнопкой или Esc. */
export function showTooPoor(): void {
  if (open) {
    return;
  }
  const overlay = el('div', 'poor');
  overlay.setAttribute('role', 'dialog');
  overlay.setAttribute('aria-modal', 'true');
  overlay.setAttribute('aria-label', caption);

  const card = el('div', 'poor-card');
  const img = el('img', 'poor-img');
  img.src = 'too-poor.webp';
  img.alt = caption;
  const close = el('button', 'primary poor-close', 'Пойду отжиматься 💪');
  card.append(img, close);
  overlay.append(card);

  const onKey = (e: KeyboardEvent): void => {
    if (e.key === 'Escape') {
      hide();
    }
  };
  const hide = (): void => {
    document.removeEventListener('keydown', onKey);
    overlay.remove();
    open = null;
  };
  overlay.addEventListener('click', hide);
  document.addEventListener('keydown', onKey);

  open = overlay;
  document.body.append(overlay);
  close.focus();
}
