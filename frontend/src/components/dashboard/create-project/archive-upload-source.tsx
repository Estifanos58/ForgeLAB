'use client';

import React from 'react';
import { Button } from '@/components/ui/button';
import { formatBytes } from '@/lib/source-utils';
import { ImportPhase } from './types';
import {
  Upload,
  RefreshCw,
  CheckCircle2,
  FileArchive,
  Sparkles,
  ArrowRight,
} from 'lucide-react';

interface ArchiveUploadSourceProps {
  zipInputRef: React.RefObject<HTMLInputElement | null>;
  importPhase: ImportPhase;
  uploadStats: string | null;
  localFolderName: string;
  localFilesCount: number;
  uploadPercent: number;
  uploadLoadedBytes: number;
  uploadTotalBytes: number;
  uploadSpeed: string | null;
  uploadEta: string | null;
  serverPhase: string;
  processedFiles: number;
  onFileChange: (e: React.ChangeEvent<HTMLInputElement>) => void;
  onCancelUpload: () => void;
  onContinueToConfig: () => void;
}

export function ArchiveUploadSource({
  zipInputRef,
  importPhase,
  uploadStats,
  localFolderName,
  localFilesCount,
  uploadPercent,
  uploadLoadedBytes,
  uploadTotalBytes,
  uploadSpeed,
  uploadEta,
  serverPhase,
  processedFiles,
  onFileChange,
  onCancelUpload,
  onContinueToConfig,
}: ArchiveUploadSourceProps) {
  return (
    <div className="space-y-4">
      <input
        type="file"
        ref={zipInputRef as any}
        accept=".zip,.tar.gz,.tgz"
        onChange={onFileChange}
        className="hidden"
      />

      {/* State: Preparing */}
      {importPhase === 'preparing' && (
        <div className="rounded-lg border border-primary/40 bg-surface-elevated/40 p-6 text-center space-y-4">
          <div className="flex items-center justify-center gap-2 text-primary font-medium text-sm">
            <RefreshCw className="w-4 h-4 animate-spin" />
            <span>Preparing Archive File</span>
          </div>
          {uploadStats && (
            <div className="inline-block px-3 py-1 rounded-full bg-surface border border-surface-border text-xs font-mono text-neutral-300">
              {uploadStats}
            </div>
          )}
          <p className="text-xs text-neutral-400 max-w-md mx-auto leading-relaxed">
            Validating archive integrity and preparing upload stream...
          </p>
          <div className="pt-2">
            <Button type="button" variant="outline" size="sm" onClick={onCancelUpload}>
              Cancel
            </Button>
          </div>
        </div>
      )}

      {/* State: Uploading */}
      {importPhase === 'uploading' && (
        <div className="rounded-lg border border-primary/40 bg-surface-elevated/40 p-6 text-center space-y-4">
          <div className="flex items-center justify-between">
            <div className="text-left">
              <div className="flex items-center gap-2 text-primary font-medium text-sm">
                <Upload className="w-4 h-4 animate-pulse" />
                <span>Uploading Source Archive</span>
              </div>
              <div className="text-xs text-neutral-400 mt-0.5">
                {localFolderName && <strong className="text-neutral-200">{localFolderName}</strong>}
              </div>
            </div>
            {uploadStats && (
              <span className="inline-block px-2.5 py-0.5 rounded-full bg-surface border border-surface-border text-[11px] font-mono text-neutral-300">
                {uploadStats}
              </span>
            )}
          </div>

          <div className="w-full space-y-2">
            <div className="w-full bg-surface-elevated rounded-full h-2.5 overflow-hidden border border-surface-border">
              <div
                className="bg-primary h-full transition-all duration-150 ease-out"
                style={{ width: `${Math.max(2, uploadPercent)}%` }}
              />
            </div>
            <div className="flex items-center justify-between text-[11px] font-mono text-neutral-400">
              <span>
                Uploading {uploadPercent}% · {formatBytes(uploadLoadedBytes)} / {formatBytes(uploadTotalBytes)}
              </span>
              <span>
                {uploadSpeed ? `${uploadSpeed} · ` : ''}{uploadEta || 'streaming'}
              </span>
            </div>
          </div>

          <div className="flex items-center justify-center pt-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onCancelUpload}
              className="text-neutral-400 hover:text-white"
            >
              Cancel Upload
            </Button>
          </div>
        </div>
      )}

      {/* State: Processing */}
      {importPhase === 'processing' && (
        <div className="rounded-lg border border-primary/40 bg-surface-elevated/40 p-6 text-center space-y-4">
          <div className="flex items-center justify-between">
            <div className="text-left">
              <div className="flex items-center gap-2 text-primary font-medium text-sm">
                <RefreshCw className="w-4 h-4 animate-spin text-primary" />
                <span>Upload Complete · Processing Source</span>
              </div>
              <div className="text-xs text-neutral-400 mt-0.5">
                {localFolderName && <strong className="text-neutral-200">{localFolderName}</strong>}
              </div>
            </div>
            {uploadStats && (
              <span className="inline-block px-2.5 py-0.5 rounded-full bg-surface border border-surface-border text-[11px] font-mono text-neutral-300">
                {uploadStats}
              </span>
            )}
          </div>

          <div className="p-4 rounded-md bg-surface border border-surface-border text-left space-y-3">
            <div className="flex items-center justify-between text-xs">
              <span className="text-neutral-300 font-medium flex items-center gap-2">
                <Sparkles className="w-3.5 h-3.5 text-amber-400" />
                {serverPhase === 'detecting'
                  ? 'Detecting project framework & services...'
                  : 'Extracting and finalizing source workspace...'}
              </span>
              <span className="font-mono text-neutral-400 text-[11px]">
                {processedFiles > 0 ? `${processedFiles.toLocaleString()} / ${localFilesCount.toLocaleString()} files` : 'Analyzing'}
              </span>
            </div>
            <div className="w-full bg-surface-elevated rounded-full h-1.5 overflow-hidden">
              <div className="bg-primary/80 h-full w-full animate-pulse" />
            </div>
          </div>
        </div>
      )}

      {/* State: Ready */}
      {importPhase === 'ready' && (
        <div className="rounded-lg border border-emerald-500/40 bg-surface-elevated/40 p-5 space-y-4">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2.5">
              <div className="w-7 h-7 rounded-full bg-emerald-500/10 border border-emerald-500/30 flex items-center justify-center text-emerald-400">
                <CheckCircle2 className="w-4 h-4" />
              </div>
              <div>
                <h4 className="text-sm font-semibold text-white">Archive Ready</h4>
                <p className="text-xs text-neutral-400">
                  {localFolderName} ({localFilesCount.toLocaleString()} files)
                </p>
              </div>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={onCancelUpload}
              className="text-xs text-neutral-400 hover:text-white"
            >
              Change Archive
            </Button>
          </div>

          <div className="flex justify-end pt-1">
            <Button
              type="button"
              variant="primary"
              size="sm"
              onClick={onContinueToConfig}
              icon={<ArrowRight className="w-3.5 h-3.5" />}
            >
              Continue to Configuration
            </Button>
          </div>
        </div>
      )}

      {/* State: Idle / Dropzone */}
      {(importPhase === 'idle' || importPhase === 'failed' || importPhase === 'cancelled') && (
        <div className="rounded-lg border-2 border-dashed border-surface-border bg-surface-elevated/20 p-8 text-center space-y-4 hover:border-neutral-600 transition-colors">
          <div className="w-10 h-10 mx-auto rounded-full bg-surface-elevated border border-surface-border flex items-center justify-center text-neutral-300">
            <FileArchive className="w-5 h-5" />
          </div>
          <div>
            <h4 className="text-sm font-semibold text-white">Upload Archive Fallback</h4>
            <p className="text-xs text-neutral-400 max-w-sm mx-auto mt-1 leading-relaxed">
              Upload a compressed archive (.zip, .tar.gz, .tgz) when the ForgeLAB agent is unavailable. The server extracts and isolates it in a secure source workspace.
            </p>
          </div>

          <div className="flex items-center justify-center gap-3 pt-2">
            <Button
              type="button"
              variant="primary"
              size="sm"
              onClick={() => zipInputRef.current?.click()}
              icon={<Upload className="w-4 h-4" />}
            >
              Choose Archive File
            </Button>
          </div>
          <p className="text-[11px] text-neutral-500 font-mono">
            Maximum archive size: 100 MB. Common build caches are pruned.
          </p>
        </div>
      )}
    </div>
  );
}
