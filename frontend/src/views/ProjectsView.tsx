// 项目 —— 数据库与升级单的归属,自己的一页。
//
// 它从"数据源配置"页里搬出来,因为这两件事的读者不是同一批人:配置实例的是 DBA,
// 而按项目跟进升级单的是业务线上的人。挤在一页里,后者要先走进一个自己用不上的
// 页面,才能看到唯一关心的那块。
//
// 页面刻意只做**归属与跟进**:每个项目名下有多少库、多少张升级单,点进去直接落到按项目
// 筛过的升级单列表。项目的增删改在管理后台 → 系统设置 → 项目(管理接口受 IP 白名单与管理后台
// 空闲超时约束,放在用户后台会被挡回来),这里的面板是只读的,管理员看到一个跳过去的入口。库归属到哪个项目仍然在数据源配置页上做 ——
// 那件事要对着实例底下的库来点,搬过来反而离开了它的上下文。
import { useState } from 'react'
import { Database, Rocket, ArrowRight } from 'lucide-react'
import ProjectsPanel from '@/components/settings/ProjectsPanel'
import api from '@/api'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { nav } from '@/hooks/useNav'
import { useUIStore } from '@/stores/ui'
import type { Project } from '@/types'
import './ProjectsView.scoped.css'

// 点一个项目 = 去看它的升级单。项目存在的理由就是跟进,所以这是这页最该有的出口。
function openReleases(p: Project) {
  nav.push({ path: '/releases', query: { project: String(p.id) } })
}

export default function ProjectsView() {
  const { t } = useI18n()
  const [projects, setProjects] = useState<Project[]>([])

  const totals = {
    projects: projects.length,
    databases: projects.reduce((n, p) => n + (p.databases || 0), 0),
    releases: projects.reduce((n, p) => n + (p.releases || 0), 0),
  }

  async function load() {
    try {
      const list = (await api.projects()) ?? []
      setProjects(list)
      useUIStore.getState().setPageSub({ key: 'prPageSub', params: { n: list.length } })
    } catch { /* 面板自己会报错,这里只是统计条降级为空 */ }
  }
  useMountEffect(() => { void load() })

  return (
    <div className="scy page">
      {/* 页眉沿用其余内容页的同一套写法(eyebrow + sub):这一页原先直接从统计卡开始,
          没有页眉,也没有页面容器 —— 内容一路顶到窗口边缘,右边第三张统计卡还被裁掉。
          在一堆都缩进、都带页眉的兄弟页面里,它看着像另一个产品的页面。 */}
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">PROJECTS</div>
          <div className="v-h1">{t('t_project')}</div>
          <div className="v-sub">{t('prPageSub', { n: totals.projects })}</div>
        </div>
      </div>

      <div className="stats">
        <div className="stat"><div className="sv">{totals.projects}</div><div className="sl">{t('prStatProjects')}</div></div>
        <div className="stat"><div className="sv">{totals.databases}</div><div className="sl">{t('prStatDatabases')}</div></div>
        <div className="stat"><div className="sv">{totals.releases}</div><div className="sl">{t('prStatReleases')}</div></div>
      </div>

      <ProjectsPanel readonly onChanged={setProjects} />

      {/* 跟进出口:每个项目一行,点进去就是按它筛过的升级单 */}
      {projects.length > 0 && (
        <div className="card">
          <div className="chead">
            <div className="cic"><Rocket size={17} color="var(--accent-text)" /></div>
            <div><div className="ct">{t('prFollowTitle')}</div><div className="cs">{t('prFollowSub')}</div></div>
          </div>
          <div className="rows">
            {projects.map((p) => (
              <div key={p.id} className="row click" onClick={() => openReleases(p)}>
                <div className="grow">
                  <div className="rn">{p.name}</div>
                  <div className="rm">
                    <span><Database size={11} />{t('prDbs', { n: p.databases })}</span>
                    <span className="sep">·</span>
                    <span><Rocket size={11} />{t('prReleases', { n: p.releases })}</span>
                  </div>
                </div>
                <span className="go">{t('prGoReleases')}<ArrowRight size={13} /></span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
