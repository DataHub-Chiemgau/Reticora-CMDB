// Download of a completed export job's file over its signed URL (EXP-01),
// shared by the CI list and the export page.

export class ExportDownloadError extends Error {}

/**
 * downloadExport saves the file behind the signed download URL of a completed
 * export job (EXP-01). The URL carries its own signature: it is fetched
 * without the session's bearer token, which never leaves the API origin. If
 * the object store refuses the cross-origin read, the browser opens the
 * signed URL itself (the object store answers CSV as a download).
 */
export async function downloadExport(url: string, filename: string) {
  // Only a web URL is followed, never e.g. a javascript: URL.
  if (!/^https?:$/.test(new URL(url, window.location.href).protocol)) {
    throw new ExportDownloadError('download URL is not http(s)');
  }
  let href = url;
  let objectUrl: string | undefined;
  try {
    const response = await fetch(url);
    if (!response.ok) {
      throw new ExportDownloadError(`download answered ${response.status}`);
    }
    objectUrl = URL.createObjectURL(await response.blob());
    href = objectUrl;
  } catch (error) {
    if (error instanceof ExportDownloadError) {
      throw error;
    }
  }
  const anchor = document.createElement('a');
  anchor.href = href;
  anchor.download = filename;
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  if (objectUrl) {
    const revoke = objectUrl;
    window.setTimeout(() => URL.revokeObjectURL(revoke), 30_000);
  }
}
