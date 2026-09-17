package service

import (
	"testing"
	"time"
)

func TestMultiAPIKeysParsing(t *testing.T) {
	a := &Account{Credentials: map[string]any{
		"api_keys": []any{" sk-1 ", "", "sk-2", "sk-1", 123, "sk-3"},
	}}
	got := a.MultiAPIKeys()
	want := []string{"sk-1", "sk-2", "sk-3"}
	if len(got) != len(want) {
		t.Fatalf("MultiAPIKeys 长度: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MultiAPIKeys[%d]: got %q, want %q", i, got[i], want[i])
		}
	}

	if k := (&Account{Credentials: map[string]any{"api_keys": []any{}}}).MultiAPIKeys(); k != nil {
		t.Fatalf("空数组应返回 nil, got %v", k)
	}
	if k := (&Account{Credentials: map[string]any{}}).MultiAPIKeys(); k != nil {
		t.Fatalf("缺字段应返回 nil, got %v", k)
	}
}

func TestPickPooledAPIKeyFallback(t *testing.T) {
	resetPooledAPIKeyStateForTest()

	solo := &Account{ID: 1, Credentials: map[string]any{"api_key": "sk-solo"}}
	if got := PickPooledAPIKey(solo); got != "sk-solo" {
		t.Fatalf("单 api_key 应原样返回, got %q", got)
	}

	single := &Account{ID: 2, Credentials: map[string]any{"api_keys": []any{"sk-only"}, "api_key": "sk-old"}}
	if got := PickPooledAPIKey(single); got != "sk-only" {
		t.Fatalf("单元素 api_keys 应返回该 key, got %q", got)
	}

	// 单 key 账号冷却为 no-op
	if CooldownPooledAPIKey(solo.ID, 429) {
		t.Fatal("单 key 账号不应发生冷却")
	}
}

func TestPoolRotationAndStableWindow(t *testing.T) {
	resetPooledAPIKeyStateForTest()
	keys := []string{"k1", "k2", "k3"}
	id := int64(1001)
	t0 := time.Now()

	first := pooledAPIKeyState.pick(id, keys, t0)
	// 稳定窗口内必须保持一致（同一次请求构建内多次读取 key）
	if got := pooledAPIKeyState.pick(id, keys, t0.Add(10*time.Millisecond)); got != first {
		t.Fatalf("稳定窗口内应返回同一 key: got %q, want %q", got, first)
	}

	// 窗口外轮换到不同 key
	second := pooledAPIKeyState.pick(id, keys, t0.Add(pooledAPIKeyStableWindow))
	third := pooledAPIKeyState.pick(id, keys, t0.Add(2*pooledAPIKeyStableWindow))
	if first == second || second == third || first == third {
		t.Fatalf("窗口外应轮换到不同 key: %s %s %s", first, second, third)
	}
}

func TestPoolCooldownSkipsKey(t *testing.T) {
	resetPooledAPIKeyStateForTest()
	keys := []string{"k1", "k2", "k3"}
	id := int64(1002)
	t0 := time.Now()

	picked := pooledAPIKeyState.pick(id, keys, t0)
	if !pooledAPIKeyState.cooldownLast(id, t0) {
		t.Fatal("冷却应生效")
	}
	// 冷却后即使仍在稳定窗口内也应立即换 key
	next := pooledAPIKeyState.pick(id, keys, t0.Add(time.Millisecond))
	if next == picked {
		t.Fatalf("冷却后不应继续选中 %q", picked)
	}
	// 后续多个窗口都不应再选中被冷却的 key
	for i := 1; i <= 3; i++ {
		k := pooledAPIKeyState.pick(id, keys, t0.Add(time.Duration(i)*pooledAPIKeyStableWindow))
		if k == picked {
			t.Fatalf("冷却期内不应选中 %q (第 %d 次)", picked, i)
		}
	}
	// 冷却过期后重新参与轮换
	after := pooledAPIKeyState.pick(id, keys, t0.Add(pooledAPIKeyCooldownDuration+pooledAPIKeyStableWindow))
	if after == "" {
		t.Fatal("冷却过期后应能选出 key")
	}
}

func TestPoolAllCoolingFallback(t *testing.T) {
	resetPooledAPIKeyStateForTest()
	keys := []string{"k1", "k2"}
	id := int64(1003)
	t0 := time.Now()

	first := pooledAPIKeyState.pick(id, keys, t0)
	pooledAPIKeyState.cooldownLast(id, t0)
	second := pooledAPIKeyState.pick(id, keys, t0.Add(pooledAPIKeyStableWindow))
	pooledAPIKeyState.cooldownLast(id, t0.Add(pooledAPIKeyStableWindow))
	if first == second {
		t.Fatalf("两次 pick 应不同: %s", first)
	}

	// 全部冷却：返回最早到期的 key（= 先冷却的 first）
	got := pooledAPIKeyState.pick(id, keys, t0.Add(2*pooledAPIKeyStableWindow))
	if got != first {
		t.Fatalf("全冷却时应返回最早到期的 %q, got %q", first, got)
	}
}

func TestCooldownStatusCodeFilter(t *testing.T) {
	resetPooledAPIKeyStateForTest()
	a := &Account{ID: 1004, Credentials: map[string]any{"api_keys": []any{"k1", "k2"}}}
	PickPooledAPIKey(a)

	if CooldownPooledAPIKey(a.ID, 400) {
		t.Fatal("400 不应触发冷却")
	}
	if !CooldownPooledAPIKey(a.ID, 429) {
		t.Fatal("429 应触发冷却")
	}
	// 冷却期内再次失败（如全冷却 fallback 中被再次选中）：刷新冷却时间，仍返回 true
	if !CooldownPooledAPIKey(a.ID, 429) {
		t.Fatal("冷却期内重复失败应刷新冷却")
	}
}
