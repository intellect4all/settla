/**
 * Standardized API error handler for portal pages.
 * Classifies errors and shows appropriate toast messages.
 */
export function useApiError(fallbackMessage = 'Request failed') {
  return function handleError(err: unknown): 'network' | 'auth' | 'unknown' {
    const isNetwork =
      (typeof navigator !== 'undefined' && !navigator.onLine) ||
      (err as any)?.message?.includes('fetch') ||
      (err as any)?.message?.includes('network');

    if (isNetwork) {
      console.error('Network error — please check your connection');
      return 'network';
    }

    const status = (err as any)?.response?.status ?? (err as any)?.status;
    if (status === 401 || status === 403) {
      console.error('Authentication error');
      return 'auth';
    }

    const msg =
      (err as any)?.data?.message ??
      (err as any)?.message ??
      fallbackMessage;
    console.error(msg);
    return 'unknown';
  };
}
