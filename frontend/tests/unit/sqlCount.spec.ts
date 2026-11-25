import { test, expect } from '@playwright/test'

import { countStatements, isMultiStatement } from '../../src/lib/sqlCount'

// 这个计数器只决定一件事:粘进终端的东西要不要先弹窗给人看一眼。
//
// 所以这组测试守的不是"和后端拆分器逐字一致",而是**两个方向都不会造成危害**:
// 数少了就走原来的路(行编辑器照样逐条过判定),数多了就多弹一次窗。
// 真正要避免的是把明显的一条说成多条(每次粘贴都弹窗,人很快就不看了),
// 以及把明显的多条说成一条(那就等于这个功能没做)。

test('单条语句,带不带分号都算一条', () => {
  expect(countStatements('SELECT 1')).toBe(1)
  expect(countStatements('SELECT 1;')).toBe(1)
  expect(countStatements('  SELECT 1 ;  \n')).toBe(1)
  expect(isMultiStatement('SELECT 1;')).toBe(false)
})

test('空白与纯注释不算语句', () => {
  expect(countStatements('')).toBe(0)
  expect(countStatements('   \n\t ')).toBe(0)
  expect(countStatements('-- 只是一句说明')).toBe(0)
  expect(countStatements('/* 说明 */')).toBe(0)
})

test('多条语句', () => {
  expect(countStatements('SELECT 1; SELECT 2;')).toBe(2)
  expect(countStatements('SELECT 1;\nUPDATE t SET a=1 WHERE id=2;\nDELETE FROM t WHERE id=3')).toBe(3)
  expect(isMultiStatement('SELECT 1; SELECT 2')).toBe(true)
})

// 字符串里的分号不是分隔符。数成两条只会多弹一次窗,但一条 SQL 被说成两条,
// 意味着每次粘一句带分号的字面量都要弹窗 —— 那个窗很快就没人看了。
test('字符串字面量里的分号不算', () => {
  expect(countStatements("SELECT 'a;b'")).toBe(1)
  expect(countStatements('SELECT "a;b" FROM t')).toBe(1)
  expect(countStatements('SELECT `a;b` FROM t')).toBe(1)
  // 成对引号是转义(标准写法),反斜杠不是 —— 与后端拆分器同一条规则。
  expect(countStatements("SELECT 'it''s; fine'")).toBe(1)
})

test('注释里的分号不算', () => {
  expect(countStatements('SELECT 1 -- 这里有个分号 ;\n')).toBe(1)
  expect(countStatements('SELECT 1 /* ; ; ; */')).toBe(1)
  expect(countStatements('-- 说明;\nSELECT 1;\n-- 又一句;\nSELECT 2;')).toBe(2)
})

// PL/SQL 整块是**一条**:后端的拆分器专门把它整块取走。按分号数会得到"二十条",
// 于是粘一个存储过程就会弹一个说着假话的窗。
test('PL/SQL 整块算一条', () => {
  const body = `CREATE OR REPLACE PROCEDURE p AS
BEGIN
  INSERT INTO t VALUES (1);
  COMMIT;
END;`
  expect(countStatements(body)).toBe(1)
  expect(countStatements('DECLARE v NUMBER; BEGIN v := 1; END;')).toBe(1)
})

// 带 DELIMITER 的一定是脚本 —— 里面的分号已经不是分隔符,数分号数出来的是错的,
// 但"这是多条"这个结论是对的。
test('DELIMITER 脚本当作多条', () => {
  const script = `DELIMITER $$
CREATE PROCEDURE p() BEGIN SELECT 1; END$$
DELIMITER ;`
  expect(isMultiStatement(script)).toBe(true)
})

// PostgreSQL 的美元引用里可以合法地放整段带分号的函数体。
test('美元引用里的分号不算', () => {
  const fn = "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql"
  expect(countStatements(fn)).toBe(1)
  expect(countStatements("SELECT $tag$ a; b $tag$")).toBe(1)
})

// 结尾多打的分号不该被数成一条空语句。
test('多余的分号不产生空语句', () => {
  expect(countStatements('SELECT 1;;')).toBe(1)
  expect(countStatements('SELECT 1;;SELECT 2;;')).toBe(2)
  expect(countStatements(';;;')).toBe(0)
})
