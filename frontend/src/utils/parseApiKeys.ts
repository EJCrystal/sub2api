// 解析 API Key 输入：一行一个（兼容逗号/分号分隔），去空白、去重。
// 返回多个 Key 时，账号凭据写入 credentials.api_keys（多 Key 池）：
// 网关按请求轮换使用，单个 Key 失败（401/403/429/5xx）时只冷却该 Key，
// 不摘整个账号；下一个请求自动切换到未冷却的 Key。
export function parseApiKeysInput(raw: string): string[] {
  return [...new Set(raw.split(/[\n,;]/).map((s) => s.trim()).filter(Boolean))]
}
