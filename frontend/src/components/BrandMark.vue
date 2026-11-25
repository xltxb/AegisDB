<script setup lang="ts">
// LOGO 的内容:配置了图片就显示图片,没配置显示原来的帆船图标。
// 外框(尺寸、圆角、渐变底)由使用处自己定;配置了图片时,使用处给外框加 .custom 去掉渐变底。
import { Sailboat } from 'lucide-vue-next'
import { brand } from '@/lib/brand'

defineProps<{ size: number }>()
</script>

<template>
  <img v-if="brand.logo" :src="brand.logo" alt="" class="brand-img" data-testid="brand-logo-img" />
  <Sailboat v-else :size="size" color="#fff" data-testid="brand-logo-default" />
</template>

<style scoped>
.brand-img { width: 100%; height: 100%; object-fit: contain; border-radius: inherit; display: block; }
</style>
