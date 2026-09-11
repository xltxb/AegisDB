import { queryOptions } from '@tanstack/react-query'
import { http, ok, type Envelope } from '@/api/shared'
import { connectionsApi } from '@/api/modules/connections'
import type {
  RoleBrief, RoleDetail, UserView,
} from '@/types'

/**
 * 角色详情,外加**可能不在响应里**的那三个字段。
 *
 * `dto.RoleDetailResp` 目前只回 id/code/name/layer/icon/menus/matrix/members/tags ——
 * `canApprove`、`defaultConnRole`、`description` 只出现在 `RoleUpdateReq` 那一侧:
 * 能写,读不回来。所以这里三个全是可选,而可选在这一页有确切含义:**未定义 = 不知道**。
 *
 * 这个区分是 issue #46 的全部内容。旧版把"不知道"按角色 code 猜成一个具体值,再随
 * 改名一起 PATCH 回去,于是"把角色改个名字"顺手把审批权关了。`undefined` 不可猜,
 * 只能不发 —— 后端对空串与 nil 的 canApprove 一律不覆盖(handler/admin.go UpdateRole)。
 */
export interface RoleDetailFull extends RoleDetail {
  canApprove?: boolean
  defaultConnRole?: string
  description?: string
}

export const permissionsApi = {
  // ---- roles ----
  roles: () => http.get<any, Envelope<RoleBrief[]>>('/roles').then(ok),
  role: (id: number) => http.get<any, Envelope<RoleDetailFull>>(`/roles/${id}`).then(ok),
  updateRole: (id: number, body: { name?: string; description?: string; defaultConnRole?: string; canApprove?: boolean }) =>
    http.patch<any, Envelope<RoleDetailFull>>(`/roles/${id}`, body).then(ok),
  setMenus: (id: number, menus: Record<string, boolean>) =>
    http.put<any, Envelope<any>>(`/roles/${id}/menus`, { menus }).then(ok),
  setCapabilities: (id: number, matrix: Record<string, Record<string, string>>) =>
    http.put<any, Envelope<any>>(`/roles/${id}/capabilities`, { matrix }).then(ok),
  setRoleTags: (id: number, tags: string[]) =>
    http.put<any, Envelope<RoleDetail>>(`/roles/${id}/tags`, { tags }).then(ok),
  addMember: (id: number, userId: number) =>
    http.post<any, Envelope<RoleDetail>>(`/roles/${id}/members`, { userId }).then(ok),
  removeMember: (id: number, userId: number) =>
    http.delete<any, Envelope<RoleDetail>>(`/roles/${id}/members/${userId}`).then(ok),

  // ---- users ----
  users: () => http.get<any, Envelope<UserView[]>>('/users').then(ok),
  // 这两条都要过 `ok()`。后端一律回 HTTP 200,业务失败藏在包体的 code 里 ——
  // 不解包的话「不能停用自己」会一路走成 mutation 的 onSuccess,界面弹一句
  // "已保存",而那个人其实还启用着。
  patchUser: (id: number, body: { status?: string; roleId?: number; roleIds?: number[] }) =>
    http.patch<any, Envelope<any>>(`/users/${id}`, body).then(ok),
  invite: (email: string, roleId: number) =>
    http.post<any, Envelope<any>>('/users/invite', { email, roleId }).then(ok),
  createUser: (body: { email: string; name?: string; password: string; roleIds: number[] }) =>
    http.post<any, Envelope<any>>('/users', body).then(ok),
  setUserRoles: (id: number, roleIds: number[]) =>
    http.patch<any, Envelope<any>>(`/users/${id}`, { roleIds }).then(ok),
  // Per-user data-access scope. An empty list clears it and the user falls back
  // to the scope their roles grant.
  userTags: (id: number) => http.get<any, Envelope<string[]>>(`/users/${id}/tags`).then(ok),
  setUserTags: (id: number, tags: string[]) =>
    http.put<any, Envelope<any>>(`/users/${id}/tags`, { tags }).then(ok),
  // admin user management: password reset + OTP binding
  setUserPassword: (id: number, password: string) =>
    http.post<any, Envelope<any>>(`/users/${id}/password`, { password }).then(ok),
  resetUserMfa: (id: number) => http.post<any, Envelope<any>>(`/users/${id}/mfa/reset`).then(ok),
  bindUserMfa: (id: number) =>
    http.post<any, Envelope<{ secret: string; otpauthUri: string }>>(`/users/${id}/mfa/bind`).then(ok),
}

// ---- TanStack Query 绑定 ----

export const rolesQueryOptions = () =>
  queryOptions({ queryKey: ['roles'] as const, queryFn: permissionsApi.roles })

/**
 * 单个角色的详情。
 *
 * 每个角色一个 key(而不是把整棵树塞进一个 `['roles','detail']`),是因为用户页
 * 要同时读**若干个**角色的矩阵去算有效权限 —— 一人多角色时那就是几条并发查询,
 * 各自独立缓存;共用一个 key 的话它们会互相覆盖。
 */
export const roleQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['role', id] as const,
    queryFn: () => permissionsApi.role(id),
    enabled: id > 0,
  })

export const usersQueryOptions = () =>
  queryOptions({ queryKey: ['users'] as const, queryFn: permissionsApi.users })

/**
 * 全部标签候选(`GET /tags`)。
 *
 * 它不是一张受管的字典表:服务端扫的是**所有实例的 `tags` 字段**,切分、转小写、
 * 去重之后回来(repository.AllConnectionTags)。所以这份清单只是"别人已经用过什么"
 * 的一个建议,不是可选值的全集 —— 标签选择器允许自由输入正是因为这一点。
 *
 * 请求本身住在 connections 模块(标签是实例的属性,那里才是它的家),这里只补一个
 * 查询绑定,好让角色页与用户页共用同一份缓存。
 */
export const allTagsQueryOptions = () =>
  queryOptions({ queryKey: ['tags'] as const, queryFn: connectionsApi.tags })

export const userTagsQueryOptions = (id: number) =>
  queryOptions({
    queryKey: ['user-tags', id] as const,
    queryFn: () => permissionsApi.userTags(id),
    enabled: id > 0,
  })
