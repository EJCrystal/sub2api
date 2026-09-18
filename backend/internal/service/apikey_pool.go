package service

import (
	"strings"
	"sync"
	"time"
)

// MultiAPIKeysCredentialKey 是账号凭据中「多 API Key 池」的字段名（字符串数组）。
// 当该字段非空且含多个 key 时：
//   - 每次（按稳定窗口）轮换使用其中一个 key；
//   - key 级失败（401/402/403/429/5xx）时只冷却该 key，而不是摘掉整个账号；
//   - 重试/后续请求自动切换到未冷却的 key。
//
// 单 key 与旧数据保持原行为：字段缺失或只有一个 key 时完全走 api_key 原逻辑。
const MultiAPIKeysCredentialKey = "api_keys"

const (
	// pooledAPIKeyCooldownDuration 单 key 失败后的冷却时长。
	// 中转商 429 限流窗口通常为分钟级，3 分钟覆盖多数场景。
	pooledAPIKeyCooldownDuration = 3 * time.Minute
	// pooledAPIKeyStableWindow 选择稳定窗口：窗口内同一账号总是返回同一个 key，
	// 保证同一次请求构建中多次读取 key（取 token 与构建头）拿到一致的值（避免桶边界抖动）。
	// 「失败换 key」不依赖窗口过期：失败冷却会立即清除本次选择，下一次 pick 直接换 key。
	pooledAPIKeyStableWindow = 500 * time.Millisecond
)

// pooledAPIKeyCooldownStatusCodes 触发 key 级冷却的上游状态码。
// 取 key/凭据级失败的子集（对齐 shouldFailoverUpstreamError 中与密钥相关的错误）：
// 401/403 凭据失效、402 余额不足、429 限流；5xx 一并冷却——多 key 池场景下
// 换 key 往往能绕过局部故障的后端。405/529 属路由/过载级问题，换 key 无意义，不冷却。
var pooledAPIKeyCooldownStatusCodes = map[int]struct{}{
	401: {},
	402: {},
	403: {},
	429: {},
	500: {},
	502: {},
	503: {},
	504: {},
}

// MultiAPIKeys 返回账号凭据中的多 key 池（已去掉空白项与重复项）。
// 返回空切片表示未配置多 key，调用方应回退到 api_key 单值逻辑。
func (a *Account) MultiAPIKeys() []string {
	if a == nil || a.Credentials == nil {
		return nil
	}
	raw, ok := a.Credentials[MultiAPIKeysCredentialKey]
	if !ok || raw == nil {
		return nil
	}
	var items []string
	switch v := raw.(type) {
	case []string:
		items = v
	case []any:
		items = make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				items = append(items, s)
			}
		}
	default:
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := strings.TrimSpace(item)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PickPooledAPIKey 返回账号当前应使用的上游 API Key。
// 池为空或只有一个 key 时等价于原 api_key 读取；多 key 时按稳定窗口轮换并跳过冷却中的 key。
func PickPooledAPIKey(a *Account) string {
	if a == nil {
		return ""
	}
	keys := a.MultiAPIKeys()
	switch len(keys) {
	case 0:
		return a.GetCredential("api_key")
	case 1:
		return keys[0]
	}
	return pooledAPIKeyState.pick(a.ID, keys, time.Now())
}

// CooldownPooledAPIKey 在账号的上游请求以指定状态码失败时，冷却该账号最近一次
// 轮换选中的 key。仅对多 key 池账号生效；返回是否实际执行了冷却。
// 由 failover 路径调用（handler 层每次决定重试/切换账号时）。
func CooldownPooledAPIKey(accountID int64, statusCode int) bool {
	if _, ok := pooledAPIKeyCooldownStatusCodes[statusCode]; !ok {
		return false
	}
	return pooledAPIKeyState.cooldownLast(accountID, time.Now())
}

type pooledAPIKeyPick struct {
	key string
	at  time.Time
}

type pooledAPIKeyPoolState struct {
	mu       sync.Mutex
	counter  map[int64]uint64               // accountID -> 轮换计数
	lastPick map[int64]pooledAPIKeyPick     // accountID -> 最近一次选中的 key 与时间
	cooldown map[int64]map[string]time.Time // accountID -> key -> 冷却截止
}

var pooledAPIKeyState = &pooledAPIKeyPoolState{
	counter:  make(map[int64]uint64),
	lastPick: make(map[int64]pooledAPIKeyPick),
	cooldown: make(map[int64]map[string]time.Time),
}

// pick 选择本次应使用的 key：
//  1. 稳定窗口内返回同一 key（保证单次请求构建的一致性）；
//  2. 窗口外轮换前进一格；被冷却的 key 跳过；
//  3. 全部冷却时返回最早到期的 key（保持账号可用，而不是直接判死）。
func (s *pooledAPIKeyPoolState) pick(accountID int64, keys []string, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(accountID, now)

	if last, ok := s.lastPick[accountID]; ok &&
		now.Sub(last.at) < pooledAPIKeyStableWindow &&
		!s.isCoolingLocked(accountID, last.key, now) {
		return last.key
	}

	s.counter[accountID]++
	start := int(s.counter[accountID] % uint64(len(keys)))
	var fallback string
	var fallbackUntil time.Time
	for i := 0; i < len(keys); i++ {
		key := keys[(start+i)%len(keys)]
		if !s.isCoolingLocked(accountID, key, now) {
			s.lastPick[accountID] = pooledAPIKeyPick{key: key, at: now}
			return key
		}
		if until := s.cooldown[accountID][key]; fallback == "" || until.Before(fallbackUntil) {
			fallback, fallbackUntil = key, until
		}
	}
	// 全部冷却：选最早到期的，赌它已恢复；失败会再次进入冷却。
	s.lastPick[accountID] = pooledAPIKeyPick{key: fallback, at: now}
	return fallback
}

// cooldownLast 冷却 lastPick 记录的 key，并清掉桶锁定，使下一次 pick 立即换 key。
func (s *pooledAPIKeyPoolState) cooldownLast(accountID int64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	last, ok := s.lastPick[accountID]
	if !ok || last.key == "" {
		return false
	}
	cds := s.cooldown[accountID]
	if cds == nil {
		cds = make(map[string]time.Time)
		s.cooldown[accountID] = cds
	}
	cds[last.key] = now.Add(pooledAPIKeyCooldownDuration)
	// 不清理 lastPick：稳定窗口检查带有冷却守卫，被冷却的 key 不会再被返回，
	// 下一次 pick 自动进入轮换分支选择未冷却的 key。
	return true
}

func (s *pooledAPIKeyPoolState) isCoolingLocked(accountID int64, key string, now time.Time) bool {
	cds := s.cooldown[accountID]
	if cds == nil || key == "" {
		return false
	}
	until, ok := cds[key]
	return ok && now.Before(until)
}

func (s *pooledAPIKeyPoolState) purgeExpiredLocked(accountID int64, now time.Time) {
	cds := s.cooldown[accountID]
	for key, until := range cds {
		if !now.Before(until) {
			delete(cds, key)
		}
	}
	if len(cds) == 0 {
		delete(s.cooldown, accountID)
	}
}

// resetPooledAPIKeyStateForTest 重置进程内状态（仅测试使用）。
func resetPooledAPIKeyStateForTest() {
	pooledAPIKeyState.mu.Lock()
	defer pooledAPIKeyState.mu.Unlock()
	pooledAPIKeyState.counter = make(map[int64]uint64)
	pooledAPIKeyState.lastPick = make(map[int64]pooledAPIKeyPick)
	pooledAPIKeyState.cooldown = make(map[int64]map[string]time.Time)
}
