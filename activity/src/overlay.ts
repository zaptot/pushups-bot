let open: HTMLDivElement | null = null;

/**
 * Оверлей поверх игры: затемнение и карточка с содержимым. Закрывается кликом в любом месте,
 * кнопкой внутри или Esc. Одновременно открыт только один; повторный вызов ничего не делает.
 * backdrop — слой между затемнением и карточкой (например, конфетти).
 */
export function showOverlay(
  className: string,
  label: string,
  content: HTMLElement[],
  focus: HTMLElement,
  backdrop?: HTMLElement,
): void {
  if (open) {
    return;
  }
  const overlay = document.createElement('div');
  overlay.className = `overlay ${className}`;
  overlay.setAttribute('role', 'dialog');
  overlay.setAttribute('aria-modal', 'true');
  overlay.setAttribute('aria-label', label);
  const card = document.createElement('div');
  card.className = 'overlay-card';
  card.append(...content);
  if (backdrop) {
    overlay.append(backdrop);
  }
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
  focus.focus();
}
