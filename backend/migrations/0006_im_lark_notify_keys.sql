-- 飞书群通知的配置键从 notify.lark* 搬到 im.lark.*(多渠道 IM 框架,见 spec §9 / §13)。
--
-- 值原样搬,不解密也不重新加密:notify.larkSecret 以前是明文存的,新键 im.lark.notifySecret
-- 读取时走 decryptSetting,它对解不开的旧明文原样返回,所以照样能用;下一次在界面上保存
-- 就会被加密。
--
-- 开关要特别处理:旧代码读 notify.lark 时默认 **true**,而生产环境不播种,所以很多库里根本
-- 没有这一行 —— 行为是「配了 webhook 就发」。新键默认 false,若只搬存在的行,这些部署升级
-- 后群通知会悄悄停掉。于是:只要配了 webhook,就写一个开关,值取旧开关,没有旧开关就是 true。

INSERT INTO tbl_setting (k, v)
SELECT 'im.lark.notify', COALESCE((SELECT v FROM tbl_setting WHERE k = 'notify.lark'), 'true')
WHERE EXISTS (SELECT 1 FROM tbl_setting WHERE k = 'notify.larkWebhook' AND v NOT IN ('', '""'))
ON CONFLICT (k) DO NOTHING;

INSERT INTO tbl_setting (k, v)
SELECT 'im.lark.notifyWebhook', v FROM tbl_setting WHERE k = 'notify.larkWebhook'
ON CONFLICT (k) DO NOTHING;

INSERT INTO tbl_setting (k, v)
SELECT 'im.lark.notifySecret', v FROM tbl_setting WHERE k = 'notify.larkSecret'
ON CONFLICT (k) DO NOTHING;

-- notify.larkChannel 是个从来没有代码读过的种子键,顺手清掉。
DELETE FROM tbl_setting WHERE k IN ('notify.lark', 'notify.larkWebhook', 'notify.larkSecret', 'notify.larkChannel');
