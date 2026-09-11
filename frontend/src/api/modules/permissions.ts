import { http, ok, type Envelope } from '../shared'
import type {
  RoleBrief, RoleDetail, UserView,
} from '@/types'

export const permissionsApi = {
  // ---- roles ----
  roles: () => http.get<any, Envelope<RoleBrief[]>>('/roles').then(ok),
  role: (id: number) => http.get<any, Envelope<RoleDetail>>(`/roles/${id}`).then(ok),
  updateRole: (id: number, body: { name?: string; description?: string; defaultConnRole?: string; canApprove?: boolean }) =>
    http.patch<any, Envelope<RoleDetail>>(`/roles/${id}`, body).then(ok),
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
  patchUser: (id: number, body: { status?: string; roleId?: number; roleIds?: number[] }) =>
    http.patch(`/users/${id}`, body),
  invite: (email: string, roleId: number) => http.post('/users/invite', { email, roleId }),
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
