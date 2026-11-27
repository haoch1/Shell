import React from 'react';
import * as TooltipPrimitive from '@radix-ui/react-tooltip';

export const TooltipProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <TooltipPrimitive.Provider delayDuration={260} skipDelayDuration={120}>
    {children}
  </TooltipPrimitive.Provider>
);

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
  if (disabled || !label) return children;
  return (
    <TooltipPrimitive.Root>
      <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
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
