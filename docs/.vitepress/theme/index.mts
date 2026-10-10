import type { Theme } from 'vitepress'
import DefaultTheme from 'vitepress/theme'
import './custom.css'

/**
 * mermaid 查看器：
 * - 正文内：图按自然尺寸渲染（CSS），超宽可拖拽平移
 * - 点击图弹出全屏查看层：＋/－/滚轮缩放、适应窗口、1:1、拖拽平移、Esc 关闭
 * MutationObserver 监听正文变化：SPA 切页、mermaid 异步渲染完成都会重新绑定。
 */
function setupMermaidViewer() {
  if (typeof window === 'undefined') return
  const w = window as any
  if (w.__mermaidViewerReady) return
  w.__mermaidViewerReady = true

  function openViewer(svg: SVGElement) {
    const overlay = document.createElement('div')
    overlay.className = 'mermaid-viewer-overlay'

    const bar = document.createElement('div')
    bar.className = 'mermaid-viewer-bar'
    const body = document.createElement('div')
    body.className = 'mermaid-viewer-body'

    const btn = (label: string, title: string, onClick: () => void) => {
      const b = document.createElement('button')
      b.textContent = label
      b.title = title
      b.addEventListener('click', (e) => {
        e.stopPropagation()
        onClick()
      })
      bar.appendChild(b)
      return b
    }

    const clone = svg.cloneNode(true) as SVGElement
    // 剥掉 mermaid 内联/属性的 width、height、max-width 限制，缩放由查看器接管
    ;['width', 'height'].forEach((a) => clone.removeAttribute(a))
    clone.style.maxWidth = 'none'
    clone.style.width = 'auto'
    clone.style.height = 'auto'
    const vb = (clone as any).viewBox?.baseVal
    const baseW = vb && vb.width ? vb.width : clone.getBoundingClientRect().width || 800
    const baseH = vb && vb.height ? vb.height : clone.getBoundingClientRect().height || 600
    body.appendChild(clone)

    let scale = 1
    const pct = document.createElement('span')
    pct.className = 'mermaid-viewer-pct'
    const apply = () => {
      clone.style.width = `${baseW * scale}px`
      clone.style.height = `${baseH * scale}px`
      pct.textContent = `${Math.round(scale * 100)}%`
    }
    const zoom = (factor: number) => {
      scale = Math.min(8, Math.max(0.1, scale * factor))
      apply()
    }
    const fit = () => {
      scale = Math.min(
        body.clientWidth / baseW,
        body.clientHeight / baseH,
        1,
      )
      apply()
      body.scrollLeft = (body.scrollWidth - body.clientWidth) / 2
      body.scrollTop = (body.scrollHeight - body.clientHeight) / 2
    }

    btn('－', '缩小', () => zoom(1 / 1.1))
    bar.appendChild(pct)
    btn('＋', '放大', () => zoom(1.1))
    btn('适应窗口', '缩放到完整可见', fit)
    btn('1:1', '原始尺寸', () => {
      scale = 1
      apply()
    })
    const close = btn('✕ 关闭', '关闭 (Esc)', () => overlay.remove())
    close.classList.add('mermaid-viewer-close')

    // 滚轮缩放
    body.addEventListener(
      'wheel',
      (e) => {
        e.preventDefault()
        zoom(e.deltaY < 0 ? 1.05 : 1 / 1.05)
      },
      { passive: false },
    )

    // 拖拽平移
    let dragging = false
    let sx = 0
    let sy = 0
    let sl = 0
    let st = 0
    body.addEventListener('pointerdown', (e) => {
      if (e.button !== 0) return
      dragging = true
      sx = e.clientX
      sy = e.clientY
      sl = body.scrollLeft
      st = body.scrollTop
    })
    window.addEventListener('pointermove', (e) => {
      if (!dragging) return
      body.scrollLeft = sl - (e.clientX - sx)
      body.scrollTop = st - (e.clientY - sy)
    })
    window.addEventListener('pointerup', () => {
      dragging = false
    })

    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        overlay.remove()
        document.removeEventListener('keydown', onKey)
      }
    }
    document.addEventListener('keydown', onKey)
    overlay.addEventListener('click', (e) => {
      if (e.target === overlay) overlay.remove()
    })

    overlay.appendChild(bar)
    overlay.appendChild(body)
    document.body.appendChild(overlay)
    fit()
  }

  const bind = () => {
    document.querySelectorAll<HTMLElement>('.mermaid').forEach((el) => {
      if (el.dataset.viewerReady) return
      el.dataset.viewerReady = '1'

      // 正文内拖拽平移
      let dragging = false
      let moved = false
      let startX = 0
      let startLeft = 0
      el.addEventListener('pointerdown', (e) => {
        if (e.button !== 0) return
        dragging = true
        moved = false
        startX = e.clientX
        startLeft = el.scrollLeft
      })
      window.addEventListener('pointermove', (e) => {
        if (!dragging) return
        const dx = e.clientX - startX
        if (Math.abs(dx) > 4) moved = true
        el.scrollLeft = startLeft - dx
      })
      window.addEventListener('pointerup', () => {
        dragging = false
      })

      // 点击（非拖拽）→ 全屏查看器
      el.addEventListener('click', () => {
        if (moved) return
        const svg = el.querySelector('svg')
        if (svg) openViewer(svg)
      })
    })
  }

  bind()
  new MutationObserver(() => bind()).observe(document.body, {
    childList: true,
    subtree: true,
  })
}

const theme: Theme = {
  extends: DefaultTheme,
  enhanceApp() {
    setupMermaidViewer()
  },
}

export default theme
