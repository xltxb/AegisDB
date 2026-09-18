/**
 * MySQL 在线表结构变更(ADR 0011)在界面这一侧的判断。
 *
 * 放在 lib 里而不是页面组件里,是因为这几件事**必须能被单独测到**:什么时候不许
 * 发起、进度怎么算、以及重启之后哪些任务是需要人去收的残局。它们各自都有一种
 * 会让人做出错误决定的失败方式。
 */

/** 五个阶段 + 三种终态,与后端 osc.JobStatus 一一对应。 */
export type OscJobStatus =
  | 'pending' | 'preflight' | 'copying' | 'replaying' | 'cutover'
  | 'done' | 'failed' | 'aborted'

export interface OscJob {
  id: number
  connectionId: number
  schema: string
  table: string
  alter: string
  status: OscJobStatus
  /** 影子表名。建出来就写,失败之后靠它找到残留。 */
  shadow: string
  copiedRows: number
  /** information_schema 的**估算**行数,可能为 0,也可能小于实际。 */
  totalRows: number
  err: string
  createdBy: string
  createdAt: string
  updatedAt: string
  finishedAt: string | null
  /**
   * **本进程此刻**有没有在推进这条任务。不落库,由接口层从网关自己手上的取消钩子填。
   *
   * 它回答的是 status 回答不了的问题:库里一条 copying,可能正在跑,也可能是上一个
   * 进程死在半路留下的。多副本共享一个库时,别的副本正在跑的任务在这里也是 false ——
   * 所以它只能说「这台网关没在推进它」,不能说「没有人在推进它」。
   */
  running: boolean
}

export interface OscStatus {
  enabled: boolean
  /** 这套东西当前还没做到的事,后端给,界面原样显示。 */
  caveats: string[]
}

const LIVE: OscJobStatus[] = ['pending', 'preflight', 'copying', 'replaying', 'cutover']

/** 这条任务还在路上(或者以为自己还在路上)。 */
export function isLive(job: OscJob): boolean {
  return LIVE.includes(job.status)
}

/**
 * 不许发起时的理由;可以发起时是 null。
 *
 * 状态**没读回来**也当作不许 —— 默认值写成"可以"的开关,在接口挂掉的那一刻就自动
 * 打开了,而那正是最不该让人按下去的时刻。
 */
export function startRefusal(status: OscStatus | undefined): string | null {
  if (!status) return 'oscStatusUnknown'
  if (!status.enabled) return 'oscDisabledHint'
  return null
}

/**
 * 拷贝进度的百分比;总行数未知时返回 null。
 *
 * 未知返回 null 而不是 0 或 100,是因为两个数字都会被读成一句确定的话:0 像"还没开始",
 * 100 像"可以走了"。null 逼着界面去说"总行数未知"。
 *
 * 上界夹到 100:估算值偏小时真会拷过头,而一个 137% 的进度条会让人怀疑别的东西。
 */
export function copyPercent(job: OscJob): number | null {
  if (job.totalRows <= 0) return null
  return Math.min(100, Math.round((job.copiedRows / job.totalRows) * 100))
}

/**
 * 需要人来收的残局。
 *
 * 两种,麻烦各不相同:
 *  1. 状态停在中间态**而且没有进程在推进它** —— 网关重启过,它不会自己走完;
 *  2. 已经失败或中止,但影子表名还在 —— 库里躺着一张表占着磁盘,自动清理没做成。
 *
 * 第一种的判据是 `running`,不是 `status`:一条死在半路的 copying 记录和一条正在跑的
 * copying 记录,状态字段一模一样。只按状态判的话残局会被当成"有人在管",而那张影子表
 * 一直躺到磁盘报警才被发现。
 *
 * 已完成的任务一律不算,哪怕它记着影子表名:切换成功之后那个名字指的是已经上位的
 * 新表,把它列成待清理会诱导人去删掉刚换上去的表。
 */
export function leftovers(jobs: OscJob[]): OscJob[] {
  return jobs.filter((j) => {
    if (isLive(j)) return !j.running
    return (j.status === 'failed' || j.status === 'aborted') && j.shadow !== ''
  })
}
