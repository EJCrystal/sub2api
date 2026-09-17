import { describe, it, expect } from 'vitest'
import { parseApiKeysInput } from '../parseApiKeys'

describe('parseApiKeysInput', () => {
  it('单个 Key 原样返回', () => {
    expect(parseApiKeysInput('sk-abc')).toEqual(['sk-abc'])
  })

  it('多行解析：trim + 去空行', () => {
    expect(parseApiKeysInput('sk-1\n  sk-2  \n\n')).toEqual(['sk-1', 'sk-2'])
  })

  it('兼容逗号/分号分隔并去重', () => {
    expect(parseApiKeysInput('sk-1, sk-1;sk-2,sk-3')).toEqual(['sk-1', 'sk-2', 'sk-3'])
  })

  it('空白输入返回空数组', () => {
    expect(parseApiKeysInput('  \n ')).toEqual([])
    expect(parseApiKeysInput('')).toEqual([])
  })
})
