const layers: HTMLElement[] = []
let originalOverflow = ''

export function isTopDialog(root: HTMLElement | null): boolean {
  return layers[layers.length - 1] === root
}

export function registerDialog(root: HTMLElement): () => void {
  const previous = document.activeElement as HTMLElement | null
  if (layers.length === 0) {
    originalOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  }
  layers.push(root)
  // 叠加确认框时，底层表单暂停接收焦点和键盘事件。
  layers.forEach((layer) => { layer.inert = layer !== root })

  return () => {
    const index = layers.indexOf(root)
    if (index >= 0) layers.splice(index, 1)
    root.inert = false
    const top = layers[layers.length - 1]
    layers.forEach((layer) => { layer.inert = layer !== top })
    // 表单和确认框可能同时卸载，最后一层关闭后再恢复页面滚动。
    if (!top) document.body.style.overflow = originalOverflow
    if (previous?.isConnected && (!top || top.contains(previous))) previous.focus()
    else if (top && !top.contains(document.activeElement)) top.focus()
  }
}
