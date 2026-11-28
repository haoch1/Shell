import React, { useEffect, useRef, useState } from 'react';
import * as TooltipPrimitive from '@radix-ui/react-tooltip';

export const TooltipProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <TooltipPrimitive.Provider delayDuration={300} skipDelayDuration={120} disableHoverableContent>
    {children}
  </TooltipPrimitive.Provider>
);

function isOccludedByModal(el: HTMLElement | null): boolean {
  const backdrops = document.querySelectorAll('.modal-backdrop');
  if (backdrops.length === 0) return false;
  const topBackdrop = backdrops[backdrops.length - 1];
  return !(el && topBackdrop.contains(el));
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
    if (next && isOccludedByModal(triggerRef.current)) {
      setOpen(false);
      return;
    }
    setOpen(next);
  };

  useEffect(() => {
    if (!open) return;
    if (disabled || !label || isOccludedByModal(triggerRef.current)) {
      setOpen(false);
      return;
    }
    const dismiss = () => setOpen(false);
    window.addEventListener('pointerdown', dismiss, true);
    window.addEventListener('keydown', dismiss, true);
    window.addEventListener('blur', dismiss);
    return () => {
      window.removeEventListener('pointerdown', dismiss, true);
      window.removeEventListener('keydown', dismiss, true);
      window.removeEventListener('blur', dismiss);
    };
  }, [open, disabled, label]);

  if (disabled || !label) return children;
  return (
    <TooltipPrimitive.Root open={open} onOpenChange={handleOpenChange}>
      <TooltipPrimitive.Trigger
        ref={triggerRef}
        asChild
        onPointerDown={() => setOpen(false)}
        onClick={() => setOpen(false)}
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
