/**
 * Maximum allowed uncompressed source size in bytes (100 MB).
 */
export const MAX_SOURCE_SIZE_BYTES = 100 * 1024 * 1024;

/**
 * Path segments that represent development caches, dependencies, or OS artifacts.
 * Filtering is strictly segment-based (case-insensitive) to prevent false positives.
 */
export const IGNORED_PATH_SEGMENTS = new Set([
  'node_modules',
  '.git',
  '.next',
  'dist',
  'build',
  '.venv',
  'venv',
  '__pycache__',
  '.cache',
  '.turbo',
  '.ds_store',
  'thumbs.db',
]);

/**
 * Checks if a relative file path contains any ignored path segments.
 * Compares path segments against IGNORED_PATH_SEGMENTS case-insensitively.
 *
 * Example:
 *   isIgnoredPath("project/node_modules/pkg/index.js") => true
 *   isIgnoredPath("project/src/building.ts")           => false
 */
export function isIgnoredPath(relativePath: string): boolean {
  if (!relativePath) return false;
  // Normalize forward and backward slashes
  const segments = relativePath.split(/[/\\]/);
  for (const segment of segments) {
    if (IGNORED_PATH_SEGMENTS.has(segment.toLowerCase())) {
      return true;
    }
  }
  return false;
}

export interface FilteredFileList {
  acceptedFiles: File[];
  totalSelectedCount: number;
  excludedCount: number;
  totalSizeBytes: number;
  detectedRootFolder: string;
}

/**
 * Efficiently filters a browser FileList, skipping ignored segments and calculating total size
 * without reading file contents or copying memory buffers.
 */
export function filterDirectoryFiles(fileList: FileList | File[]): FilteredFileList {
  const acceptedFiles: File[] = [];
  let excludedCount = 0;
  let totalSizeBytes = 0;
  let detectedRootFolder = '';

  const total = fileList.length;
  for (let i = 0; i < total; i++) {
    const file = fileList[i];
    const relPath = file.webkitRelativePath || file.name;

    if (!detectedRootFolder && relPath.includes('/')) {
      detectedRootFolder = relPath.split('/')[0];
    }

    if (isIgnoredPath(relPath)) {
      excludedCount++;
    } else {
      acceptedFiles.push(file);
      totalSizeBytes += file.size;
    }
  }

  return {
    acceptedFiles,
    totalSelectedCount: total,
    excludedCount,
    totalSizeBytes,
    detectedRootFolder: detectedRootFolder || 'local-app',
  };
}

/**
 * Formats byte count into a human-readable string (e.g. "42.7 MB").
 */
export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`;
}
