export function pemFileError(file: File | null) {
  if (!file || file.size === 0) return 'Select a PEM file.';
  return file.size > 256 * 1024 ? 'Choose a PEM file smaller than 256 KiB.' : null;
}
