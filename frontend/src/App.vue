<script setup lang="ts">
import { watchEffect } from 'vue'
import Toasts from '@/components/common/Toasts.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { brand, brandText, docTitle, setBrand, titleCount } from '@/lib/brand'

const ui = useUIStore()

// 系统名称与 LOGO:登录页未登录时就要显示,所以启动时就拉(公开接口)。拉不到就保持默认,不打扰人。
api.branding().then(setBrand).catch(() => {})

// 浏览器标题:品牌名(跟着界面语言)+ 待办数前缀(AppLayout 给)。
watchEffect(() => { document.title = docTitle(brandText(ui.lang), titleCount.value) })

// favicon 跟着 LOGO 换;清掉 LOGO 就回到 index.html 里原来那个。
const iconLink = document.querySelector<HTMLLinkElement>('link[rel~="icon"]')
const defaultIcon = iconLink?.getAttribute('href') || ''
watchEffect(() => { if (iconLink) iconLink.setAttribute('href', brand.logo || defaultIcon) })
</script>

<template>
  <router-view />
  <Toasts />
  <ConfirmDialog />
</template>
