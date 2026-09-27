import React from 'react';
import { cn } from '@/lib/utils/cn';

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  label?: string;
  error?: string;
  helperText?: string;
  icon?: React.ReactNode;
}

export const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ className, type = 'text', label, error, helperText, icon, id, required, ...props }, ref) => {
    const inputId = id || (label ? label.toLowerCase().replace(/\s+/g, '-') : undefined);

    return (
      <div className="w-full space-y-1">
        {label && (
          <label htmlFor={inputId} className="block text-xs font-medium text-neutral-300">
            {label} {required && <span className="text-red-400">*</span>}
          </label>
        )}
        <div className="relative">
          {icon && (
            <div className="absolute inset-y-0 left-0 pl-2.5 flex items-center pointer-events-none text-neutral-500">
              {icon}
            </div>
          )}
          <input
            id={inputId}
            type={type}
            ref={ref}
            required={required}
            className={cn(
              'w-full h-8 sm:h-9 rounded-md bg-[#0a0a0c] border border-surface-border px-3 text-xs sm:text-sm text-neutral-100 placeholder-neutral-500 transition-colors',
              'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-neutral-400 focus-visible:border-neutral-400',
              'disabled:opacity-40 disabled:cursor-not-allowed',
              icon && 'pl-8',
              error && 'border-red-500/70 focus-visible:ring-red-400 focus-visible:border-red-400',
              className
            )}
            {...props}
          />
        </div>
        {error ? (
          <p className="text-[11px] text-red-400 leading-tight">{error}</p>
        ) : helperText ? (
          <p className="text-[11px] text-neutral-500 leading-tight">{helperText}</p>
        ) : null}
      </div>
    );
  }
);

Input.displayName = 'Input';
