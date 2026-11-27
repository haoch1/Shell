import React from 'react';
import * as SelectPrimitive from '@radix-ui/react-select';
import { Check, ChevronDown } from 'lucide-react';

const EMPTY_SENTINEL = '__empty__';

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface SelectProps {
  value: string;
  onValueChange: (value: string) => void;
  options: SelectOption[];
  placeholder?: string;
  id?: string;
  size?: 'sm' | 'md';
  className?: string;
  style?: React.CSSProperties;
  disabled?: boolean;
  ariaLabel?: string;
}

export const Select: React.FC<SelectProps> = ({
  value,
  onValueChange,
  options,
  placeholder,
  id,
  size = 'md',
  className = '',
  style,
  disabled = false,
  ariaLabel,
}) => {
  const hasEmptyOption = options.some((o) => o.value === '');
  const radixValue = value === '' ? (hasEmptyOption ? EMPTY_SENTINEL : undefined) : value;

  return (
    <SelectPrimitive.Root
      value={radixValue}
      onValueChange={(next) => onValueChange(next === EMPTY_SENTINEL ? '' : next)}
      disabled={disabled}
    >
      <SelectPrimitive.Trigger
        id={id}
        className={`ui-select-trigger ${size === 'sm' ? 'ui-select-sm' : ''} ${className}`.trim()}
        style={style}
        aria-label={ariaLabel}
      >
        <span className="ui-select-value">
          <SelectPrimitive.Value placeholder={placeholder} />
        </span>
        <SelectPrimitive.Icon className="ui-select-icon">
          <ChevronDown size={13.5} strokeWidth={2} />
        </SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>

      <SelectPrimitive.Portal>
        <SelectPrimitive.Content
          className="ui-select-content"
          position="popper"
          sideOffset={5}
          collisionPadding={8}
          onEscapeKeyDown={(e) => e.stopPropagation()}
          onClick={(e) => e.stopPropagation()}
        >
          <SelectPrimitive.Viewport className="ui-select-viewport">
            {options.map((opt) => {
              const itemValue = opt.value === '' ? EMPTY_SENTINEL : opt.value;
              return (
                <SelectPrimitive.Item
                  key={itemValue}
                  value={itemValue}
                  disabled={opt.disabled}
                  className="ui-select-item"
                >
                  <SelectPrimitive.ItemText>{opt.label}</SelectPrimitive.ItemText>
                  <SelectPrimitive.ItemIndicator className="ui-select-check">
                    <Check size={13} strokeWidth={2.2} />
                  </SelectPrimitive.ItemIndicator>
                </SelectPrimitive.Item>
              );
            })}
          </SelectPrimitive.Viewport>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  );
};
