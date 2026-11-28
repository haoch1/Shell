import React, { useEffect, useRef, useState } from 'react';
import * as TooltipPrimitive from '@radix-ui/react-tooltip';
import {
  ensureWindowFocusGuardInstalled,
  isWindowInactive,
  isWindowRestoringFocus,
  shouldOpenTooltipOnFocus,
  subscribeWindowDismiss,
} from '../../lib/windowFocusGuard';

export const TooltipProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  useEffect(() => {
    ensureWindowFocusGuardInstalled();
  }, []);

  return (
    <TooltipPrimitive.Provider delayDuration={300} skipDelayDuration={120} disableHoverableContent>
      {children}
    </TooltipPrimitive.Provider>
  );
};

function isOccludedByModal(el: HTMLElement | null): boolean {
  const backdrops = document.querySelectorAll('.modal-backdrop');
  if (backdrops.length === 0) return false;
  const topBackdrop = backdrops[backdrops.length - 1];
  return !(el && topBackdrop.contains(el));
}

function matchesFocusVisible(el: HTMLElement | null): boolean {
  if (!el) return false;
  try {
    return el.matches(':focus-visible');
  } catch {
    return false;
  }
}

interface TooltipProps {
  label: React.ReactNode;
  shortcut?: string;
  side?: 'top' | 'right' | 'bottom' | 'left';
  align?: 'start' | 'center' | 'end';
  sideOffset?: number;
  disabled?: boolean;
  children: React.ReactElement;
}

export const Tooltip: React.FC<TooltipProps> = ({
  label,
  shortcut,
  side = 'top',
  align = 'center',
  sideOffset = 6,
  disabled = false,
  children,
}) => {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);

  const handleOpenChange = (next: boolean) => {
    if (next && (document.hidden || isWindowInactive() || isOccludedByModal(triggerRef.current))) {
      setOpen(false);
      return;
    }
    setOpen(next);
  };

  useEffect(() => {
    if (!open) return;
    if (disabled || !label || document.hidden || isOccludedByModal(triggerRef.current)) {
      setOpen(false);
      return;
    }
    const dismiss = () => setOpen(false);
    const offWindowDismiss = subscribeWindowDismiss(dismiss);
    window.addEventListener('pointerdown', dismiss, true);
    window.addEventListener('keydown', dismiss, true);
    window.addEventListener('resize', dismiss);
    return () => {
      offWindowDismiss();
      window.removeEventListener('pointerdown', dismiss, true);
      window.removeEventListener('keydown', dismiss, true);
      window.removeEventListener('resize', dismiss);
    };
  }, [open, disabled, label]);

  if (disabled || !label) return children;
  return (
    <TooltipPrimitive.Root open={open} onOpenChange={handleOpenChange}>
      <TooltipPrimitive.Trigger
        ref={triggerRef}
        asChild
        data-tooltip-trigger=""
        onPointerDown={() => setOpen(false)}
        onClick={() => setOpen(false)}
        onFocus={(e) => {
          const el = e.currentTarget as HTMLElement | null;
          if (!shouldOpenTooltipOnFocus(matchesFocusVisible(el))) {
            // Prevent Radix TooltipTrigger's built-in onFocus from popping open
            // on window minimize/restore, tray reopen, Alt+Tab, or modal close.
            e.preventDefault();
            setOpen(false);
            if (isWindowRestoringFocus() && el) {
              el.blur();
            }
          }
        }}
      >
        {children}
      </TooltipPrimitive.Trigger>
      <TooltipPrimitive.Portal>
        <TooltipPrimitive.Content
          className="ui-tooltip-content"
          side={side}
          align={align}
          sideOffset={sideOffset}
          collisionPadding={8}
        >
          <span>{label}</span>
          {shortcut && <kbd className="ui-tooltip-kbd">{shortcut}</kbd>}
        </TooltipPrimitive.Content>
      </TooltipPrimitive.Portal>
    </TooltipPrimitive.Root>
  );
};
