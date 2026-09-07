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
  <div class="scy page">
    <!-- 页眉沿用其余内容页的同一套写法(eyebrow + sub):这一页原先直接从统计卡开始,
         没有页眉,也没有页面容器 —— 内容一路顶到窗口边缘,右边第三张统计卡还被裁掉。
         在一堆都缩进、都带页眉的兄弟页面里,它看着像另一个产品的页面。 -->
    <div class="head">
      <div>
        <div class="eyebrow">PROJECTS</div>
        <div class="sub">{{ $t('prPageSub', { n: totals.projects }) }}</div>
      </div>
    </div>

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
/* 与其余内容页同源:留白、最大宽度、超宽屏居中都来自同两个 token。 */
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; display: flex; flex-direction: column; gap: 16px; }
.head { display: flex; align-items: center; margin-bottom: 2px; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
/* auto-fit 而不是写死三列:窄屏上三张并排会把"已归属的数据库"这类标签挤到换行,
   放不下时自己换行更稳。 */
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 14px; }
.stat { padding: 16px 20px; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); }
.sv { font: 700 26px var(--font-display); color: var(--text-strong); }
.sl { margin-top: 2px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.card { border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.chead { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.cic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.ct { font: 600 14px var(--font-display); color: var(--text-strong); }
.cs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
/* 与上面那块面板(ProjectsPanel .list/.item)取同一套行样式:同一页里出现的是同一种
   东西 —— 一个项目 —— 从前上面是带边框的沉底行、下面是纯白无分隔的行,两份列表并排,
   看着像两个页面拼起来的。内边距、圆角、底色、间距都对齐它。 */
.rows { padding: 14px 20px; display: flex; flex-direction: column; gap: 8px; }
.row {
  display: flex; align-items: center; gap: 12px; padding: 10px 12px;
  border: 1px solid var(--border-default); border-radius: 11px; background: var(--surface-sunken);
  transition: border-color var(--dur-fast, 0.15s) var(--ease-out, ease);
}
.row.click { cursor: pointer; }
.row.click:hover { border-color: var(--accent-text); }
.grow { flex: 1; min-width: 0; }
.rn { font: 600 13px var(--font-body); color: var(--text-strong); }
.rm { margin-top: 3px; display: flex; align-items: center; gap: 7px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.rm span { display: inline-flex; align-items: center; gap: 4px; }
.sep { color: var(--text-faint); }
.go { display: inline-flex; align-items: center; gap: 4px; font: 600 11.5px var(--font-body); color: var(--accent-text); opacity: 0; transition: opacity 0.12s; }
.row.click:hover .go { opacity: 1; }
</style>
