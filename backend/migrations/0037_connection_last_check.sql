-- 0037: 把「上一次巡检的结论」记在连接上。
--
-- ## 为什么
--
-- 在此之前,`status` 是连接上唯一和"健康"沾边的字段,而它其实只有两个值:online 和
-- maint —— **人为标的维护态**,和"连不连得上"没有任何关系。
--
-- 于是数据源页那个叫「仅看异常」的筛选,筛的是 status,它**永远筛不出连不上的实例**。
-- 两台新加的、根本连不上的库,在页面上和健康的库长得一模一样。
--
-- 更糟的是:连通性其实**探过**。新建连接、编辑保存、批量巡检都会打 /connections/:id/test,
-- 而那个接口从 2026-09-10 起是真的拨号的。探完之后结论只活在那一次 HTTP 响应里,
-- 随手就扔了。
--
-- 这一笔不改 status 的含义 —— 它继续表示维护态。连通性是**另一件事实**,单独记。
--
-- ## 三列,缺一不可
--
--   last_check_at   什么时候探的。没有它,一个"连不上"会被当成此刻的事实,而它可能
--                   是三天前的 —— 与元数据缓存必须显示年龄是同一个道理。
--   last_check_ok   探测结论。**可空**:NULL = 从没探过,和"探过、通了"是两回事,
--                   页面上必须分开说。用 TINYINT 而不是 BOOLEAN,与本库其它布尔列一致。
--   last_check_err  失败原因原样留着。"i/o timeout"和"Access denied"指向完全不同的
--                   处置动作,压成一句"连不上"等于把唯一有用的信息丢了。
--
-- 成功时 err 清空 —— 留着上次的错误会让一台已经好了的实例永远显示成坏的
-- (与 tbl_meta_sync 同一条规矩)。
ALTER TABLE tbl_connection
  ADD COLUMN last_check_at  DATETIME(3)  NULL,
  ADD COLUMN last_check_ok  TINYINT(1)   NULL,
  ADD COLUMN last_check_err VARCHAR(512) NOT NULL DEFAULT '';
