<script setup lang="ts">
// 全屏水印:当前用户 · 账号 · 日期,斜向平铺在登录后的每一页上。
//
// ## 它是给截图看的,不是给页面看的
//
// 一张从这个控制台截出去的图(审批卡、命令原文、执行结果)如果流到了不该去的地方,
// 水印回答的是"谁截的、什么时候"。所以内容必须是**当前登录者**的身份加日期,不能是
// 一句固定的口号 —— 口号谁截都一样,追不回来。
//
// ## 怎么画
//
// 用 canvas 画一块瓦片(旋转 -22°,低透明度),转成 data URI 做 background-image,
// 铺满一个 position: fixed、pointer-events: none 的层。不用一堆 DOM 节点平铺:那样
// 每次滚动都要参与布局,而一张背景图对布局是零成本。按 devicePixelRatio 放大画布,
// 高分屏上文字才不糊。
//
// 颜色按主题走:明色用深字、暗色用浅字,透明度都很低 —— 它要能被看见,又不能干扰读
// 页面上的字。
//
// ## 防手动摘掉
//
// 一个 MutationObserver 盯着父节点:层被删就补回去,style 被改就改回来。这挡不住会开
// 调试器的人(那不是水印能解决的问题),挡的是"随手一删再截图"。

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'

const auth = useAuthStore()
const ui = useUIStore()

const root = ref<HTMLElement | null>(null)
const image = ref('')

// 一行身份,一行日期。邮箱是账号 —— 同名的人可以有两个,账号只有一个。
const lines = computed(() => {
  const me = auth.me
  if (!me) return [] as string[]
  const day = new Date().toISOString().slice(0, 10)
  return [`${me.name} · ${me.email}`, day]
})

function paint() {
  if (!lines.value.length) { image.value = ''; return }
  const dark = ui.resolvedTheme() === 'dark'
  const dpr = Math.min(window.devicePixelRatio || 1, 2)
  const w = 320, h = 200
  const c = document.createElement('canvas')
  c.width = w * dpr
  c.height = h * dpr
  const g = c.getContext('2d')
  if (!g) return
  g.scale(dpr, dpr)
  g.translate(w / 2, h / 2)
  g.rotate(-22 * Math.PI / 180)
  g.textAlign = 'center'
  g.textBaseline = 'middle'
  g.fillStyle = dark ? 'rgba(255,255,255,0.075)' : 'rgba(15,23,42,0.075)'
  g.font = '600 13px system-ui, -apple-system, "Segoe UI", sans-serif'
  g.fillText(lines.value[0], 0, -9)
  g.font = '500 11px ui-monospace, Menlo, Consolas, monospace'
  g.fillText(lines.value[1], 0, 10)
  image.value = c.toDataURL('image/png')
}

// 主题可能三种方式变:应用内切换(ui.theme)、系统偏好变化(system 模式下)、以及
// 直接改 data-theme(测试/截图工具会这么做)。三条都接上,水印颜色才不会和页面反着来。
let media: MediaQueryList | null = null
let themeObserver: MutationObserver | null = null
let guard: MutationObserver | null = null
const baseStyle = 'position:fixed;inset:0;z-index:2147483000;pointer-events:none;background-repeat:repeat;'

function applyStyle() {
  if (!root.value) return
  root.value.setAttribute('style', baseStyle + (image.value ? `background-image:url("${image.value}")` : ''))
}

onMounted(() => {
  paint()
  applyStyle()
  media = window.matchMedia('(prefers-color-scheme: dark)')
  media.addEventListener?.('change', () => { paint(); applyStyle() })
  themeObserver = new MutationObserver(() => { paint(); applyStyle() })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })

  // 防摘:层被删就补回原位,style 被改就改回来。
  const parent = root.value?.parentElement
  if (parent && root.value) {
    const el = root.value
    guard = new MutationObserver(() => {
      if (!parent.contains(el)) parent.appendChild(el)
      if (el.getAttribute('style') !== baseStyle + (image.value ? `background-image:url("${image.value}")` : '')) applyStyle()
    })
    guard.observe(parent, { childList: true })
    guard.observe(el, { attributes: true, attributeFilter: ['style', 'class', 'hidden'] })
  }
})
onBeforeUnmount(() => {
  themeObserver?.disconnect()
  guard?.disconnect()
})
watch([lines, () => ui.theme], () => { paint(); applyStyle() })
</script>

<template>
  <div ref="root" class="watermark" aria-hidden="true" />
</template>
