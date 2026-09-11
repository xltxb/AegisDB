import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { authApi } from '@/api/modules/auth'

/**
 * 取一份待绑定的密钥 + otpauth URI。
 *
 * 是 POST,却当查询用:它**不改账号状态**(真正生效在 enable 那一步),每次打开
 * 弹窗都该拿一份新的 —— 上一次没绑完就关掉的密钥,再拿出来用没有任何好处。
 * 所以 `gcTime: 0`,关掉弹窗这份缓存就没了。
 *
 * `enabled` 由调用方把"弹窗开着 + 还没绑过"两件事都算进去:已经绑过的账号调
 * setup,服务端回的是 403(要先关掉再重绑),那不是一次该发出去的请求。
 */
export function useMfaSetup(enabled: boolean) {
  return useQuery({
    queryKey: ['mfa-setup'] as const,
    queryFn: authApi.mfaSetup,
    enabled,
    gcTime: 0,
    staleTime: Infinity,
    retry: false,
  })
}

/**
 * 绑定 / 解绑都要失效 `me` —— 账户菜单上那个「已绑定 / 未绑定」读的就是它,
 * 而 meQueryOptions 的 staleTime 是 Infinity,不主动失效它永远停在旧值上。
 */
export function useEnableMfa() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (code: string) => authApi.mfaEnable(code),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  })
}

export function useDisableMfa() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (code: string) => authApi.mfaDisable(code),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  })
}
