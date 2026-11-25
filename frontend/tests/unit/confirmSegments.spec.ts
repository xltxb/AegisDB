import { test, expect } from '@playwright/test'
import { confirmSegments } from '../../src/lib/confirm'

// 确认弹窗把文案里「…」包着的命令 / 名字切出来按等宽高亮,其余照原文。

test('「…」切成高亮段,前后文字原样保留', () => {
  expect(confirmSegments("现在真正执行「GRANT SELECT ON `a`.* TO 'u'@'%';」吗?只能执行一次。")).toEqual([
    { text: '现在真正执行', code: false },
    { text: "GRANT SELECT ON `a`.* TO 'u'@'%';", code: true },
    { text: '吗?只能执行一次。', code: false },
  ])
})

test('多个「…」、开头结尾就是「…」、没有「…」', () => {
  expect(confirmSegments('「a」和「b」')).toEqual([
    { text: 'a', code: true }, { text: '和', code: false }, { text: 'b', code: true },
  ])
  expect(confirmSegments('删除这个窗口?')).toEqual([{ text: '删除这个窗口?', code: false }])
  expect(confirmSegments('只有半个「括号')).toEqual([{ text: '只有半个「括号', code: false }])
})
