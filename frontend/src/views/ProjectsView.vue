<script setup lang="ts">
// 项目 —— 数据库与升级单的归属,自己的一页。
//
// 它从"数据源配置"页里搬出来,因为这两件事的读者不是同一批人:配置实例的是 DBA,
// 而按项目跟进升级单的是业务线上的人。挤在一页里,后者要先走进一个自己用不上的
// 页面,才能看到唯一关心的那块。
//
// 页面刻意只做**归属与跟进**:项目的增删改,以及每个项目名下有多少库、多少张升级单,
// 点进去直接落到按项目筛过的升级单列表。库归属到哪个项目仍然在数据源配置页上做 ——
// 那件事要对着实例底下的库来点,搬过来反而离开了它的上下文。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Database, Rocket, ArrowRight } from 'lucide-vue-next'
import ProjectsPanel from '@/components/settings/ProjectsPanel.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import type { Project } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const router = useRouter()

const projects = ref<Project[]>([])
const panel = ref<InstanceType<typeof ProjectsPanel> | null>(null)

const totals = computed(() => ({
  projects: projects.value.length,
  databases: projects.value.reduce((n, p) => n + (p.databases || 0), 0),
  releases: projects.value.reduce((n, p) => n + (p.releases || 0), 0),
}))

async function load() {
  try {
    projects.value = await api.projects()
    ui.pageSub = t('prPageSub', { n: projects.value.length })
  } catch { /* 面板自己会报错,这里只是统计条降级为空 */ }
}
onMounted(load)

// 点一个项目 = 去看它的升级单。项目存在的理由就是跟进,所以这是这页最该有的出口。
function openReleases(p: Project) {
  router.push({ path: '/releases', query: { project: String(p.id) } })
}
</script>

<template>
  <div class="wrap">
    <div class="stats">
      <div class="stat"><div class="sv">{{ totals.projects }}</div><div class="sl">{{ $t('prStatProjects') }}</div></div>
      <div class="stat"><div class="sv">{{ totals.databases }}</div><div class="sl">{{ $t('prStatDatabases') }}</div></div>
      <div class="stat"><div class="sv">{{ totals.releases }}</div><div class="sl">{{ $t('prStatReleases') }}</div></div>
    </div>

    <ProjectsPanel ref="panel" @changed="projects = $event" />

    <!-- 跟进出口:每个项目一行,点进去就是按它筛过的升级单 -->
    <div v-if="projects.length" class="card">
      <div class="chead">
        <div class="cic"><Rocket :size="17" color="var(--accent-text)" /></div>
        <div><div class="ct">{{ $t('prFollowTitle') }}</div><div class="cs">{{ $t('prFollowSub') }}</div></div>
      </div>
      <div class="rows">
        <div v-for="p in projects" :key="p.id" class="row click" @click="openReleases(p)">
          <div class="grow">
            <div class="rn">{{ p.name }}</div>
            <div class="rm">
              <span><Database :size="11" />{{ $t('prDbs', { n: p.databases }) }}</span>
              <span class="sep">·</span>
              <span><Rocket :size="11" />{{ $t('prReleases', { n: p.releases }) }}</span>
            </div>
          </div>
          <span class="go">{{ $t('prGoReleases') }}<ArrowRight :size="13" /></span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 16px; }
.stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 14px; }
.stat { padding: 16px 20px; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); }
.sv { font: 700 26px var(--font-display); color: var(--text-strong); }
.sl { margin-top: 2px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.card { border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.chead { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.cic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.ct { font: 600 14px var(--font-display); color: var(--text-strong); }
.cs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.rows { padding: 8px 12px 12px; display: flex; flex-direction: column; gap: 6px; }
.row { display: flex; align-items: center; gap: 12px; padding: 11px 12px; border-radius: 11px; }
.row.click { cursor: pointer; }
.row.click:hover { background: var(--surface-sunken); }
.grow { flex: 1; min-width: 0; }
.rn { font: 600 13px var(--font-body); color: var(--text-strong); }
.rm { margin-top: 3px; display: flex; align-items: center; gap: 7px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.rm span { display: inline-flex; align-items: center; gap: 4px; }
.sep { color: var(--text-faint); }
.go { display: inline-flex; align-items: center; gap: 4px; font: 600 11.5px var(--font-body); color: var(--accent-text); opacity: 0; transition: opacity 0.12s; }
.row.click:hover .go { opacity: 1; }
</style>
