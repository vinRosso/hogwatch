export type ToastKind = 'info' | 'error' | 'saved'

export const toasts = $state({
  items: [] as { id: number; text: string; kind: ToastKind }[],
})

let next = 1

export function toast(text: string, kind: ToastKind = 'info', ms = 4000) {
  const id = next++
  toasts.items.push({ id, text, kind })
  setTimeout(() => dismissToast(id), ms)
}

export function dismissToast(id: number) {
  const i = toasts.items.findIndex((x) => x.id === id)
  if (i >= 0) toasts.items.splice(i, 1)
}
