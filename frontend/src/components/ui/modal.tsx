'use client';

import React, { useEffect, useRef } from 'react';
import { X } from 'lucide-react';
import { cn } from '@/lib/utils/cn';

export interface ModalProps {
  isOpen: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  children: React.ReactNode;
  maxWidth?: 'sm' | 'md' | 'lg' | 'xl' | '2xl';
}

export function Modal({ isOpen, onClose, title, description, children, maxWidth = 'md' }: ModalProps) {
  const dialogRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose();
      }
    };

    if (isOpen) {
      document.body.style.overflow = 'hidden';
      window.addEventListener('keydown', handleKeyDown);
    }

    return () => {
      document.body.style.overflow = 'unset';
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [isOpen, onClose]);

  if (!isOpen) return null;

  const maxWidths = {
    sm: 'max-w-sm',
    md: 'max-w-md',
    lg: 'max-w-lg',
    xl: 'max-w-xl',
    '2xl': 'max-w-2xl',
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-3 sm:p-4">
      {/* Backdrop */}
      <div
        className="fixed inset-0 bg-black/60 transition-opacity animate-in fade-in duration-150"
        onClick={onClose}
        aria-hidden="true"
      />

      {/* Modal Dialog Panel - Constrained to viewport height */}
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        className={cn(
          'relative w-full max-h-[calc(100vh-2rem)] flex flex-col rounded-lg bg-surface border border-surface-border shadow-popover z-10 animate-in fade-in zoom-in-[0.98] duration-150 overflow-hidden',
          maxWidths[maxWidth]
        )}
      >
        {/* Fixed Header */}
        <div className="flex items-start justify-between p-5 sm:p-6 pb-3.5 border-b border-surface-border flex-shrink-0 bg-surface">
          <div>
            <h3 id="modal-title" className="text-sm sm:text-base font-semibold text-white tracking-tight">
              {title}
            </h3>
            {description && <p className="mt-1 text-xs text-neutral-400">{description}</p>}
          </div>
          <button
            onClick={onClose}
            className="text-neutral-400 hover:text-white p-1 rounded hover:bg-surface-elevated transition-colors -mr-1 -mt-1"
            aria-label="Close dialog"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Scrollable Content Body */}
        <div className="p-5 sm:p-6 pt-4 overflow-y-auto flex-1 min-h-0 overscroll-contain">
          {children}
        </div>
      </div>
    </div>
  );
}
